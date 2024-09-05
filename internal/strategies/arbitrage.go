package strategies

import (
	"fmt"
	"math/big"
	"time"

	"mev_simulator/internal/models"
)

// ArbitrageDetector identifies arbitrage opportunities from transaction sets.
// It looks for price discrepancies across DEX pools within the same block/slot
// or across chains.
type ArbitrageDetector struct {
	// minProfitWei is the minimum profit threshold in wei (Ethereum) or lamports (Solana).
	minProfitWei *big.Int
}

// NewArbitrageDetector creates a detector with the given minimum profit threshold.
func NewArbitrageDetector(minProfitWei *big.Int) *ArbitrageDetector {
	if minProfitWei == nil {
		minProfitWei = big.NewInt(1e15) // 0.001 ETH default
	}
	return &ArbitrageDetector{minProfitWei: minProfitWei}
}

// Analyze scans a block's transactions for arbitrage opportunities.
// It identifies cases where the same token pair is traded at different prices
// within the same block, indicating a potential arbitrage path.
func (d *ArbitrageDetector) Analyze(block *models.Block) []models.MEVOpportunity {
	var opportunities []models.MEVOpportunity

	// Collect all swap-like transactions and group them by token pairs.
	swapGroups := d.groupSwapsByTokenPair(block)

	for pair, swaps := range swapGroups {
		if len(swaps) < 2 {
			continue
		}

		// Look for price discrepancies within the group.
		opp := d.findPriceDiscrepancy(pair, swaps, block)
		if opp != nil && opp.IsProfitable() && opp.Profit.Cmp(d.minProfitWei) >= 0 {
			opportunities = append(opportunities, *opp)
		}
	}

	return opportunities
}

// tokenPair is a canonical representation of two tokens being traded.
type tokenPair struct {
	token0 string
	token1 string
}

// groupSwapsByTokenPair groups swap transactions by the tokens involved.
func (d *ArbitrageDetector) groupSwapsByTokenPair(block *models.Block) map[tokenPair][]models.Transaction {
	groups := make(map[tokenPair][]models.Transaction)

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		if !isSwap(tx) {
			continue
		}

		// Extract token addresses from the transaction.
		// For real implementations, this would decode the calldata or logs.
		pair := extractTokenPairFromLogs(tx)
		if pair.token0 == "" || pair.token1 == "" {
			continue
		}

		// Ensure canonical ordering.
		if pair.token0 > pair.token1 {
			pair.token0, pair.token1 = pair.token1, pair.token0
		}

		groups[pair] = append(groups[pair], *tx)
	}

	return groups
}

// findPriceDiscrepancy analyzes swap transactions in the same token pair
// to detect profitable arbitrage routes.
func (d *ArbitrageDetector) findPriceDiscrepancy(pair tokenPair, swaps []models.Transaction, block *models.Block) *models.MEVOpportunity {
	if len(swaps) < 2 {
		return nil
	}

	// Estimate effective prices from transaction values.
	// In production, this would decode pool reserves from event logs.
	var maxValue, minValue *big.Int
	var maxTx, minTx *models.Transaction

	for i := range swaps {
		tx := &swaps[i]
		if tx.Value == nil || tx.Value.Sign() == 0 {
			continue
		}

		if maxValue == nil || tx.Value.Cmp(maxValue) > 0 {
			maxValue = tx.Value
			maxTx = tx
		}
		if minValue == nil || tx.Value.Cmp(minValue) < 0 {
			minValue = tx.Value
			minTx = tx
		}
	}

	if maxTx == nil || minTx == nil || maxTx.Hash == minTx.Hash {
		return nil
	}

	// The arbitrage profit is the price differential minus gas costs.
	priceDiff := new(big.Int).Sub(maxValue, minValue)
	gasCost := estimateGasCost(maxTx, minTx)

	profit := new(big.Int).Sub(priceDiff, gasCost)

	return &models.MEVOpportunity{
		ID:       fmt.Sprintf("arb-%s-%d", pair.token0[:8], block.Number),
		Strategy: models.StrategyArbitrage,
		Chain:    block.Chain,
		Transactions: []models.Transaction{
			*minTx, // buy low
			*maxTx, // sell high
		},
		Revenue:     priceDiff,
		Cost:        gasCost,
		Profit:      profit,
		BlockNumber: block.Number,
		Timestamp:   block.Timestamp,
		Details: map[string]string{
			"token0":     pair.token0,
			"token1":     pair.token1,
			"buy_tx":     minTx.Hash,
			"sell_tx":    maxTx.Hash,
			"price_diff": priceDiff.String(),
		},
	}
}

// AnalyzeCrossChain looks for arbitrage between Ethereum and Solana blocks
// that occurred at approximately the same time.
func (d *ArbitrageDetector) AnalyzeCrossChain(ethBlock, solBlock *models.Block) []models.CrossChainOpportunity {
	var opportunities []models.CrossChainOpportunity

	ethOpps := d.Analyze(ethBlock)
	solOpps := d.Analyze(solBlock)

	// Match opportunities that involve similar token pairs across chains.
	for i := range ethOpps {
		for j := range solOpps {
			ethOpp := &ethOpps[i]
			solOpp := &solOpps[j]

			// Check if token pairs overlap (simplified: compare detail keys).
			if ethOpp.Details["token0"] == solOpp.Details["token0"] {
				combined := new(big.Int).Add(ethOpp.Profit, solOpp.Profit)
				bridgeCost := big.NewInt(5e15) // estimated bridge cost ~0.005 ETH

				opp := models.CrossChainOpportunity{
					EthereumOpp:    ethOpp,
					SolanaOpp:      solOpp,
					CombinedProfit: combined,
					BridgeCost:     bridgeCost,
					TimeDelta:      ethBlock.Timestamp.Sub(solBlock.Timestamp),
					Feasible:       combined.Cmp(bridgeCost) > 0,
				}
				opportunities = append(opportunities, opp)
			}
		}
	}

	return opportunities
}

// isSwap checks whether a transaction looks like a DEX swap based on input data length
// and common patterns.
func isSwap(tx *models.Transaction) bool {
	return len(tx.Input) >= 4 || len(tx.Logs) > 2
}

// extractTokenPairFromLogs attempts to extract token addresses from event logs.
// In practice, this decodes Swap events from Uniswap/Raydium logs.
func extractTokenPairFromLogs(tx *models.Transaction) tokenPair {
	var pair tokenPair

	// Look for Transfer events (topic0 = keccak256("Transfer(address,address,uint256)"))
	transferTopic := "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

	tokens := make(map[string]bool)
	for _, log := range tx.Logs {
		if len(log.Topics) > 0 && log.Topics[0] == transferTopic {
			tokens[log.Address] = true
		}
	}

	addrs := make([]string, 0, len(tokens))
	for addr := range tokens {
		addrs = append(addrs, addr)
	}

	if len(addrs) >= 2 {
		pair.token0 = addrs[0]
		pair.token1 = addrs[1]
	}

	return pair
}

// estimateGasCost calculates the total gas cost for executing arbitrage transactions.
func estimateGasCost(txs ...*models.Transaction) *big.Int {
	total := big.NewInt(0)
	for _, tx := range txs {
		if tx.GasPrice != nil && tx.GasUsed > 0 {
			cost := new(big.Int).Mul(tx.GasPrice, big.NewInt(int64(tx.GasUsed)))
			total.Add(total, cost)
		} else if tx.GasPrice != nil {
			// Estimate gas at 150k for a swap.
			cost := new(big.Int).Mul(tx.GasPrice, big.NewInt(150_000))
			total.Add(total, cost)
		}
	}
	return total
}

// Strategy returns the strategy type for interface compliance.
func (d *ArbitrageDetector) Strategy() models.StrategyType {
	return models.StrategyArbitrage
}

// Name returns a human-readable name for this strategy.
func (d *ArbitrageDetector) Name() string {
	return "DEX Arbitrage Detector"
}

// MinBlockTime returns the minimum block time window for this strategy to be viable.
func (d *ArbitrageDetector) MinBlockTime() time.Duration {
	return 0 // Arbitrage is intra-block, no minimum time needed.
}
