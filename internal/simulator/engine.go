package simulator

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"sync"
	"time"

	"mev_simulator/config"
	"mev_simulator/internal/models"
)

// MEVStrategy is the interface that all MEV detection strategies must implement.
type MEVStrategy interface {
	Analyze(block *models.Block) []models.MEVOpportunity
	Strategy() models.StrategyType
	Name() string
}

// BlockFetcher abstracts the retrieval of blocks from any chain.
type BlockFetcher interface {
	FetchBlock(ctx context.Context, number uint64) (*models.Block, error)
}

// EthereumBlockFetcher wraps the Ethereum block analyzer to satisfy BlockFetcher.
type EthereumBlockFetcher struct {
	Fetcher interface {
		FetchBlock(ctx context.Context, blockNum uint64) (*models.Block, error)
	}
}

func (f *EthereumBlockFetcher) FetchBlock(ctx context.Context, number uint64) (*models.Block, error) {
	return f.Fetcher.FetchBlock(ctx, number)
}

// SolanaBlockFetcher wraps the Solana slot analyzer to satisfy BlockFetcher.
type SolanaBlockFetcher struct {
	Fetcher interface {
		FetchSlot(ctx context.Context, slotNum uint64) (*models.Block, error)
	}
}

func (f *SolanaBlockFetcher) FetchBlock(ctx context.Context, number uint64) (*models.Block, error) {
	return f.Fetcher.FetchSlot(ctx, number)
}

// Engine orchestrates the MEV simulation by fetching blocks, replaying
// transactions, and evaluating strategies.
type Engine struct {
	cfg        config.SimulationConfig
	strategies []MEVStrategy
	ethFetcher BlockFetcher
	solFetcher BlockFetcher
	mu         sync.Mutex
	results    []models.MEVOpportunity
}

// NewEngine creates a simulation engine with the given configuration and fetchers.
func NewEngine(cfg config.SimulationConfig, ethFetcher BlockFetcher, solFetcher BlockFetcher) *Engine {
	return &Engine{
		cfg:        cfg,
		ethFetcher: ethFetcher,
		solFetcher: solFetcher,
	}
}

// RegisterStrategy adds an MEV strategy to the engine's evaluation pipeline.
func (e *Engine) RegisterStrategy(strategy MEVStrategy) {
	e.strategies = append(e.strategies, strategy)
}

// RunEthereum executes the simulation across a range of Ethereum blocks.
func (e *Engine) RunEthereum(ctx context.Context, startBlock, endBlock uint64) (*models.SimulationResult, error) {
	return e.runChain(ctx, models.ChainEthereum, e.ethFetcher, startBlock, endBlock)
}

// RunSolana executes the simulation across a range of Solana slots.
func (e *Engine) RunSolana(ctx context.Context, startSlot, endSlot uint64) (*models.SimulationResult, error) {
	return e.runChain(ctx, models.ChainSolana, e.solFetcher, startSlot, endSlot)
}

// runChain runs the simulation on a single chain across the specified range.
func (e *Engine) runChain(ctx context.Context, chain models.Chain, fetcher BlockFetcher, start, end uint64) (*models.SimulationResult, error) {
	if fetcher == nil {
		return nil, fmt.Errorf("no fetcher configured for %s", chain)
	}

	startTime := time.Now()

	result := &models.SimulationResult{
		TotalRevenue:      big.NewInt(0),
		TotalCost:         big.NewInt(0),
		TotalProfit:       big.NewInt(0),
		StrategyBreakdown: make(map[models.StrategyType]models.StrategyStats),
	}

	if chain == models.ChainEthereum {
		result.StartBlock = start
		result.EndBlock = end
	} else {
		result.StartSlot = start
		result.EndSlot = end
	}

	// Use a worker pool to fetch and analyze blocks concurrently.
	workers := e.cfg.Workers
	if workers <= 0 {
		workers = 1
	}

	blockCh := make(chan uint64, workers*2)
	var wg sync.WaitGroup

	// Producer: enqueue block numbers.
	go func() {
		defer close(blockCh)
		for num := start; num <= end; num++ {
			select {
			case <-ctx.Done():
				return
			case blockCh <- num:
			}
		}
	}()

	// Workers: fetch and analyze blocks.
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for blockNum := range blockCh {
				select {
				case <-ctx.Done():
					return
				default:
				}

				block, err := fetcher.FetchBlock(ctx, blockNum)
				if err != nil {
					log.Printf("[%s] skipping block %d: %v", chain, blockNum, err)
					continue
				}

				opps := e.analyzeBlock(block)

				mu.Lock()
				result.TotalTransactions += len(block.Transactions)
				for _, opp := range opps {
					result.Opportunities = append(result.Opportunities, opp)
					if opp.Revenue != nil {
						result.TotalRevenue.Add(result.TotalRevenue, opp.Revenue)
					}
					if opp.Cost != nil {
						result.TotalCost.Add(result.TotalCost, opp.Cost)
					}
					if opp.Profit != nil {
						result.TotalProfit.Add(result.TotalProfit, opp.Profit)
					}

					stats := result.StrategyBreakdown[opp.Strategy]
					stats.Strategy = opp.Strategy
					stats.Count++
					if stats.TotalRevenue == nil {
						stats.TotalRevenue = big.NewInt(0)
						stats.TotalCost = big.NewInt(0)
						stats.TotalProfit = big.NewInt(0)
					}
					if opp.Revenue != nil {
						stats.TotalRevenue.Add(stats.TotalRevenue, opp.Revenue)
					}
					if opp.Cost != nil {
						stats.TotalCost.Add(stats.TotalCost, opp.Cost)
					}
					if opp.Profit != nil {
						stats.TotalProfit.Add(stats.TotalProfit, opp.Profit)
					}
					result.StrategyBreakdown[opp.Strategy] = stats
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	result.Duration = time.Since(startTime)

	// Calculate averages.
	for st, stats := range result.StrategyBreakdown {
		if stats.Count > 0 {
			stats.AvgProfitPerOp = new(big.Int).Div(stats.TotalProfit, big.NewInt(int64(stats.Count)))
			profitable := 0
			for _, opp := range result.Opportunities {
				if opp.Strategy == st && opp.IsProfitable() {
					profitable++
				}
			}
			stats.SuccessRate = float64(profitable) / float64(stats.Count)
		}
		result.StrategyBreakdown[st] = stats
	}

	return result, nil
}

// analyzeBlock runs all registered strategies against a single block.
func (e *Engine) analyzeBlock(block *models.Block) []models.MEVOpportunity {
	var all []models.MEVOpportunity
	for _, strategy := range e.strategies {
		opps := strategy.Analyze(block)
		all = append(all, opps...)
	}
	return all
}

// ReplayTransaction simulates the execution of a transaction within a block context.
// It returns the trace of internal calls and the estimated gas usage.
func (e *Engine) ReplayTransaction(tx *models.Transaction, block *models.Block) (*models.TransactionTrace, error) {
	if tx == nil || block == nil {
		return nil, fmt.Errorf("transaction and block must not be nil")
	}

	trace := &models.TransactionTrace{
		TxHash:  tx.Hash,
		Chain:   block.Chain,
		GasUsed: tx.GasUsed,
	}

	// Simulate internal calls based on transaction characteristics.
	if len(tx.Input) >= 4 {
		// The transaction calls a contract; simulate a top-level CALL.
		call := models.TraceCall{
			Type:    "CALL",
			From:    tx.From,
			To:      tx.To,
			Value:   tx.Value,
			GasUsed: tx.GasUsed,
			Input:   tx.Input,
			Depth:   0,
		}
		trace.Calls = append(trace.Calls, call)

		// If it's a swap, simulate the inner token transfer calls.
		if isSwap(tx) {
			trace.Calls = append(trace.Calls, models.TraceCall{
				Type:    "CALL",
				From:    tx.To,
				To:      tx.From,
				GasUsed: tx.GasUsed / 3,
				Depth:   1,
			})
		}
	}

	if tx.Status == models.TxStatusFailed {
		trace.Reverted = true
	}

	trace.Depth = maxDepth(trace.Calls)

	return trace, nil
}

// isSwap checks if a transaction looks like a DEX swap.
func isSwap(tx *models.Transaction) bool {
	return len(tx.Input) >= 4 || len(tx.Logs) > 2
}

// maxDepth returns the maximum call depth in a trace.
func maxDepth(calls []models.TraceCall) int {
	max := 0
	for _, c := range calls {
		if c.Depth > max {
			max = c.Depth
		}
	}
	return max
}
