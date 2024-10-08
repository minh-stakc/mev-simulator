package strategies

import (
	"fmt"
	"math/big"

	"mev_simulator/internal/models"
)

// LiquidationDetector identifies liquidation opportunities from on-chain
// lending protocol events. It looks for positions with health factors below
// the liquidation threshold.
type LiquidationDetector struct {
	minProfitWei           *big.Int
	liquidationThreshold   *big.Float // health factor below which liquidation is possible
	liquidationBonusPct    float64    // bonus percentage for liquidators (e.g., 5%)
}

// NewLiquidationDetector creates a detector with configurable thresholds.
func NewLiquidationDetector(minProfitWei *big.Int) *LiquidationDetector {
	if minProfitWei == nil {
		minProfitWei = big.NewInt(1e15)
	}
	return &LiquidationDetector{
		minProfitWei:         minProfitWei,
		liquidationThreshold: big.NewFloat(1.0),
		liquidationBonusPct:  5.0,
	}
}

// Analyze scans a block for liquidation events and opportunities.
func (d *LiquidationDetector) Analyze(block *models.Block) []models.MEVOpportunity {
	var opportunities []models.MEVOpportunity

	// Phase 1: Detect already-executed liquidations.
	executedLiqs := d.detectExecutedLiquidations(block)
	opportunities = append(opportunities, executedLiqs...)

	// Phase 2: Identify positions that became liquidatable in this block
	// based on price oracle updates.
	potentialLiqs := d.detectLiquidatablePositions(block)
	opportunities = append(opportunities, potentialLiqs...)

	return opportunities
}

// detectExecutedLiquidations finds transactions that are liquidation calls.
func (d *LiquidationDetector) detectExecutedLiquidations(block *models.Block) []models.MEVOpportunity {
	var opportunities []models.MEVOpportunity

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		if !isLiquidationCall(tx) {
			continue
		}

		profit := d.estimateLiquidationProfit(tx)
		gasCost := estimateGasCost(tx)
		netProfit := new(big.Int).Sub(profit, gasCost)

		if netProfit.Cmp(d.minProfitWei) < 0 {
			continue
		}

		opp := models.MEVOpportunity{
			ID:           fmt.Sprintf("liq-%s-%d", tx.Hash[:12], block.Number),
			Strategy:     models.StrategyLiquidation,
			Chain:        block.Chain,
			Transactions: []models.Transaction{*tx},
			Revenue:      profit,
			Cost:         gasCost,
			Profit:       netProfit,
			BlockNumber:  block.Number,
			Timestamp:    block.Timestamp,
			Details: map[string]string{
				"liquidation_tx": tx.Hash,
				"liquidator":     tx.From,
				"protocol":       detectProtocol(tx),
				"type":           "executed",
			},
		}
		opportunities = append(opportunities, opp)
	}

	return opportunities
}

// detectLiquidatablePositions looks for oracle price updates that push
// positions below the liquidation threshold.
func (d *LiquidationDetector) detectLiquidatablePositions(block *models.Block) []models.MEVOpportunity {
	var opportunities []models.MEVOpportunity

	// Find oracle price update events.
	priceUpdates := d.findPriceUpdates(block)
	if len(priceUpdates) == 0 {
		return nil
	}

	// For each price update, check if any known positions become liquidatable.
	// In production, this would query on-chain state for all positions.
	for _, update := range priceUpdates {
		targets := d.simulateLiquidationTargets(update, block)
		for _, target := range targets {
			profit := d.calculateLiquidationRevenue(&target)
			gasCost := big.NewInt(500_000) // estimated gas for liquidation call
			if block.BaseFee != nil {
				gasCost.Mul(gasCost, block.BaseFee)
			} else {
				gasCost.Mul(gasCost, big.NewInt(30e9)) // 30 gwei fallback
			}

			netProfit := new(big.Int).Sub(profit, gasCost)
			if netProfit.Cmp(d.minProfitWei) < 0 {
				continue
			}

			opp := models.MEVOpportunity{
				ID:       fmt.Sprintf("liq-potential-%s-%d", target.Borrower[:8], block.Number),
				Strategy: models.StrategyLiquidation,
				Chain:    block.Chain,
				Revenue:  profit,
				Cost:     gasCost,
				Profit:   netProfit,
				BlockNumber: block.Number,
				Timestamp:   block.Timestamp,
				Details: map[string]string{
					"borrower":         target.Borrower,
					"protocol":         target.Protocol,
					"collateral_token": target.CollateralToken,
					"debt_token":       target.DebtToken,
					"health_factor":    target.HealthFactor.String(),
					"type":             "potential",
				},
			}
			opportunities = append(opportunities, opp)
		}
	}

	return opportunities
}

// priceUpdate represents a detected oracle price change event.
type priceUpdate struct {
	token    string
	oldPrice *big.Int
	newPrice *big.Int
	txIndex  int
}

// findPriceUpdates looks for Chainlink or protocol oracle update events.
func (d *LiquidationDetector) findPriceUpdates(block *models.Block) []priceUpdate {
	var updates []priceUpdate

	// Chainlink AnswerUpdated event topic.
	answerUpdatedTopic := "0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f"

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		for _, log := range tx.Logs {
			if len(log.Topics) > 0 && log.Topics[0] == answerUpdatedTopic {
				update := priceUpdate{
					token:   log.Address,
					txIndex: i,
				}
				// Decode price from log data if available.
				if len(log.Data) >= 32 {
					update.newPrice = new(big.Int).SetBytes(log.Data[:32])
				}
				updates = append(updates, update)
			}
		}
	}

	return updates
}

// simulateLiquidationTargets returns positions that became liquidatable
// after the given price update. In production, this would query protocol state.
func (d *LiquidationDetector) simulateLiquidationTargets(update priceUpdate, block *models.Block) []models.LiquidationTarget {
	// This is a simulation placeholder. A real implementation would:
	// 1. Maintain a local copy of borrower positions from protocol events.
	// 2. Apply the price update to recalculate health factors.
	// 3. Return positions that fell below the liquidation threshold.
	return nil
}

// calculateLiquidationRevenue computes the expected revenue from liquidating a position.
func (d *LiquidationDetector) calculateLiquidationRevenue(target *models.LiquidationTarget) *big.Int {
	if target.DebtValue == nil || target.DebtValue.Sign() == 0 {
		return big.NewInt(0)
	}

	// Liquidation revenue = debt repaid * liquidation bonus percentage.
	// Most protocols allow liquidating up to 50% of the debt.
	halfDebt := new(big.Int).Div(target.DebtValue, big.NewInt(2))
	bonus := new(big.Int).Mul(halfDebt, big.NewInt(int64(d.liquidationBonusPct*100)))
	bonus.Div(bonus, big.NewInt(10000))

	return bonus
}

// estimateLiquidationProfit estimates the profit from an executed liquidation tx.
func (d *LiquidationDetector) estimateLiquidationProfit(tx *models.Transaction) *big.Int {
	// Heuristic: look at the value transferred in the transaction.
	if tx.Value != nil && tx.Value.Sign() > 0 {
		// Assume liquidation bonus is ~5% of the transferred value.
		bonus := new(big.Int).Mul(tx.Value, big.NewInt(5))
		bonus.Div(bonus, big.NewInt(100))
		return bonus
	}

	// Fallback: estimate from gas usage (liquidations typically use 300k-800k gas).
	if tx.GasUsed > 300_000 {
		return big.NewInt(1e16) // estimate 0.01 ETH profit
	}

	return big.NewInt(0)
}

// isLiquidationCall checks if a transaction's function selector matches
// known lending protocol liquidation functions.
func isLiquidationCall(tx *models.Transaction) bool {
	if len(tx.Input) < 4 {
		return false
	}

	selector := fmt.Sprintf("%x", tx.Input[:4])

	liquidationSelectors := map[string]bool{
		"00f714ce": true, // liquidationCall (Aave V2)
		"e3ead59e": true, // liquidationCall (Aave V3)
		"f5e3c462": true, // liquidateBorrow (Compound)
		"26c54eea": true, // liquidatePosition (various)
		"96b5a755": true, // liquidate (Euler)
	}

	return liquidationSelectors[selector]
}

// detectProtocol attempts to identify which lending protocol a liquidation
// transaction targets based on the destination address.
func detectProtocol(tx *models.Transaction) string {
	knownProtocols := map[string]string{
		"0x7d2768de32b0b80b7a3454c06bdac94a69ddc7a9": "Aave V2",
		"0x87870bca3f3fd6335c3f4ce8392d69350b4fa4e2": "Aave V3",
		"0x3d9819210a31b4961b30ef54be2aed79b9c9cd3b": "Compound",
	}

	if protocol, ok := knownProtocols[tx.To]; ok {
		return protocol
	}
	return "unknown"
}

// Strategy returns the strategy type.
func (d *LiquidationDetector) Strategy() models.StrategyType {
	return models.StrategyLiquidation
}

// Name returns a human-readable name.
func (d *LiquidationDetector) Name() string {
	return "Liquidation Opportunity Detector"
}
