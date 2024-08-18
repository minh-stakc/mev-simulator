package simulator

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"sort"
	"time"

	"mev_simulator/internal/models"
)

// CrossChainAnalyzer coordinates MEV analysis across Ethereum and Solana,
// looking for opportunities that span both chains.
type CrossChainAnalyzer struct {
	engine *Engine
}

// NewCrossChainAnalyzer creates a cross-chain analyzer backed by the simulation engine.
func NewCrossChainAnalyzer(engine *Engine) *CrossChainAnalyzer {
	return &CrossChainAnalyzer{engine: engine}
}

// TimePair associates an Ethereum block with a Solana slot that occurred
// at approximately the same time.
type TimePair struct {
	EthBlock  *models.Block
	SolSlot   *models.Block
	TimeDelta time.Duration
}

// AnalyzeWindow runs cross-chain analysis over a time-aligned window of
// Ethereum blocks and Solana slots.
func (a *CrossChainAnalyzer) AnalyzeWindow(
	ctx context.Context,
	ethStart, ethEnd uint64,
	solStart, solEnd uint64,
) (*models.SimulationResult, error) {
	startTime := time.Now()

	// Fetch Ethereum blocks.
	ethBlocks, err := a.fetchBlocks(ctx, models.ChainEthereum, ethStart, ethEnd)
	if err != nil {
		return nil, fmt.Errorf("fetching Ethereum blocks: %w", err)
	}

	// Fetch Solana slots.
	solBlocks, err := a.fetchBlocks(ctx, models.ChainSolana, solStart, solEnd)
	if err != nil {
		return nil, fmt.Errorf("fetching Solana slots: %w", err)
	}

	// Align blocks by timestamp.
	pairs := a.alignByTimestamp(ethBlocks, solBlocks)
	log.Printf("aligned %d time-paired block/slot combinations", len(pairs))

	result := &models.SimulationResult{
		StartBlock:        ethStart,
		EndBlock:          ethEnd,
		StartSlot:         solStart,
		EndSlot:           solEnd,
		TotalRevenue:      big.NewInt(0),
		TotalCost:         big.NewInt(0),
		TotalProfit:       big.NewInt(0),
		StrategyBreakdown: make(map[models.StrategyType]models.StrategyStats),
	}

	// Analyze each chain independently.
	for _, block := range ethBlocks {
		result.TotalTransactions += len(block.Transactions)
		opps := a.engine.analyzeBlock(block)
		result.Opportunities = append(result.Opportunities, opps...)
	}

	for _, block := range solBlocks {
		result.TotalTransactions += len(block.Transactions)
		opps := a.engine.analyzeBlock(block)
		result.Opportunities = append(result.Opportunities, opps...)
	}

	// Cross-chain analysis on aligned pairs.
	for _, pair := range pairs {
		crossOpps := a.analyzePair(pair)
		result.CrossChainOpps = append(result.CrossChainOpps, crossOpps...)
	}

	// Aggregate profitability.
	for _, opp := range result.Opportunities {
		if opp.Revenue != nil {
			result.TotalRevenue.Add(result.TotalRevenue, opp.Revenue)
		}
		if opp.Cost != nil {
			result.TotalCost.Add(result.TotalCost, opp.Cost)
		}
		if opp.Profit != nil {
			result.TotalProfit.Add(result.TotalProfit, opp.Profit)
		}
	}

	for _, cc := range result.CrossChainOpps {
		if cc.CombinedProfit != nil {
			result.TotalProfit.Add(result.TotalProfit, cc.CombinedProfit)
		}
	}

	result.Duration = time.Since(startTime)

	return result, nil
}

// fetchBlocks retrieves blocks from the appropriate chain fetcher.
func (a *CrossChainAnalyzer) fetchBlocks(ctx context.Context, chain models.Chain, start, end uint64) ([]*models.Block, error) {
	var fetcher BlockFetcher
	switch chain {
	case models.ChainEthereum:
		fetcher = a.engine.ethFetcher
	case models.ChainSolana:
		fetcher = a.engine.solFetcher
	default:
		return nil, fmt.Errorf("unsupported chain: %s", chain)
	}

	if fetcher == nil {
		return nil, fmt.Errorf("no fetcher configured for %s", chain)
	}

	blocks := make([]*models.Block, 0, end-start+1)
	for num := start; num <= end; num++ {
		select {
		case <-ctx.Done():
			return blocks, ctx.Err()
		default:
		}

		block, err := fetcher.FetchBlock(ctx, num)
		if err != nil {
			log.Printf("[%s] skipping %d: %v", chain, num, err)
			continue
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// alignByTimestamp pairs Ethereum blocks with Solana slots that occurred
// within a close time window (default: 12 seconds, one Ethereum block).
func (a *CrossChainAnalyzer) alignByTimestamp(ethBlocks, solBlocks []*models.Block) []TimePair {
	const maxDelta = 12 * time.Second
	var pairs []TimePair

	// Sort both by timestamp.
	sort.Slice(ethBlocks, func(i, j int) bool {
		return ethBlocks[i].Timestamp.Before(ethBlocks[j].Timestamp)
	})
	sort.Slice(solBlocks, func(i, j int) bool {
		return solBlocks[i].Timestamp.Before(solBlocks[j].Timestamp)
	})

	solIdx := 0
	for _, eth := range ethBlocks {
		// Advance Solana index to the closest matching slot.
		for solIdx < len(solBlocks) && solBlocks[solIdx].Timestamp.Before(eth.Timestamp.Add(-maxDelta)) {
			solIdx++
		}

		// Check nearby Solana slots for a match.
		for j := solIdx; j < len(solBlocks); j++ {
			delta := eth.Timestamp.Sub(solBlocks[j].Timestamp)
			if delta < 0 {
				delta = -delta
			}
			if delta > maxDelta {
				break
			}

			pairs = append(pairs, TimePair{
				EthBlock:  eth,
				SolSlot:   solBlocks[j],
				TimeDelta: delta,
			})
		}
	}

	return pairs
}

// analyzePair evaluates cross-chain MEV opportunities between a paired
// Ethereum block and Solana slot.
func (a *CrossChainAnalyzer) analyzePair(pair TimePair) []models.CrossChainOpportunity {
	var opportunities []models.CrossChainOpportunity

	ethOpps := a.engine.analyzeBlock(pair.EthBlock)
	solOpps := a.engine.analyzeBlock(pair.SolSlot)

	// Look for matching strategy types across chains.
	for i := range ethOpps {
		for j := range solOpps {
			if ethOpps[i].Strategy != solOpps[j].Strategy {
				continue
			}

			combined := big.NewInt(0)
			if ethOpps[i].Profit != nil {
				combined.Add(combined, ethOpps[i].Profit)
			}
			if solOpps[j].Profit != nil {
				combined.Add(combined, solOpps[j].Profit)
			}

			// Estimate bridge cost for moving capital between chains.
			bridgeCost := estimateBridgeCost(pair.TimeDelta)

			netProfit := new(big.Int).Sub(combined, bridgeCost)

			opp := models.CrossChainOpportunity{
				EthereumOpp:    &ethOpps[i],
				SolanaOpp:      &solOpps[j],
				CombinedProfit: netProfit,
				BridgeCost:     bridgeCost,
				TimeDelta:      pair.TimeDelta,
				Feasible:       netProfit.Sign() > 0,
			}
			opportunities = append(opportunities, opp)
		}
	}

	return opportunities
}

// estimateBridgeCost estimates the cost of bridging assets between Ethereum
// and Solana. Faster bridges cost more.
func estimateBridgeCost(timeDelta time.Duration) *big.Int {
	baseCost := big.NewInt(5e15) // 0.005 ETH base bridge cost

	// Urgency premium: tighter time windows require faster (more expensive) bridges.
	if timeDelta < 2*time.Second {
		// Near-instant bridge needed (e.g., centralized exchange).
		premium := big.NewInt(2e16) // 0.02 ETH premium
		return new(big.Int).Add(baseCost, premium)
	}
	if timeDelta < 6*time.Second {
		premium := big.NewInt(1e16)
		return new(big.Int).Add(baseCost, premium)
	}

	return baseCost
}
