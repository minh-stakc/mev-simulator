package strategies

import (
	"fmt"
	"math/big"

	"mev_simulator/internal/models"
)

// SandwichDetector identifies sandwich attack opportunities within a block.
// A sandwich attack consists of:
//  1. Front-run: buy the target token before the victim's swap
//  2. Victim: the target transaction that moves the price
//  3. Back-run: sell the token after the victim's swap at a higher price
type SandwichDetector struct {
	minProfitWei *big.Int
	maxSlippage  float64 // maximum acceptable slippage percentage
}

// NewSandwichDetector creates a detector with configurable thresholds.
func NewSandwichDetector(minProfitWei *big.Int, maxSlippage float64) *SandwichDetector {
	if minProfitWei == nil {
		minProfitWei = big.NewInt(1e15) // 0.001 ETH
	}
	if maxSlippage <= 0 {
		maxSlippage = 5.0 // 5%
	}
	return &SandwichDetector{
		minProfitWei: minProfitWei,
		maxSlippage:  maxSlippage,
	}
}

// SandwichCandidate represents a potential sandwich attack triplet.
type SandwichCandidate struct {
	FrontRun models.Transaction
	Victim   models.Transaction
	BackRun  models.Transaction
	Profit   *big.Int
	GasCost  *big.Int
}

// Analyze scans a block's transactions for sandwich attack patterns.
func (d *SandwichDetector) Analyze(block *models.Block) []models.MEVOpportunity {
	var opportunities []models.MEVOpportunity

	candidates := d.detectCandidates(block)
	for _, candidate := range candidates {
		opp := d.evaluateCandidate(candidate, block)
		if opp != nil && opp.IsProfitable() && opp.Profit.Cmp(d.minProfitWei) >= 0 {
			opportunities = append(opportunities, *opp)
		}
	}

	return opportunities
}

// detectCandidates scans for the classic sandwich pattern: two transactions
// from the same sender surrounding a victim transaction, all involving swaps.
func (d *SandwichDetector) detectCandidates(block *models.Block) []SandwichCandidate {
	var candidates []SandwichCandidate
	txs := block.Transactions

	for i := 0; i+2 < len(txs); i++ {
		front := &txs[i]
		victim := &txs[i+1]
		back := &txs[i+2]

		// Pattern: same sender for front and back, different sender for victim.
		if front.From != back.From || front.From == victim.From {
			continue
		}

		// All three must be swap-like transactions.
		if !isSwap(front) || !isSwap(victim) || !isSwap(back) {
			continue
		}

		// Estimate the profit from the sandwich.
		profit, gasCost := d.estimateSandwichProfit(front, victim, back)

		candidates = append(candidates, SandwichCandidate{
			FrontRun: *front,
			Victim:   *victim,
			BackRun:  *back,
			Profit:   profit,
			GasCost:  gasCost,
		})
	}

	return candidates
}

// estimateSandwichProfit estimates the profit from a sandwich attack.
// The attacker profits from the price impact: they buy before the victim
// pushes the price up, then sell at the elevated price.
func (d *SandwichDetector) estimateSandwichProfit(front, victim, back *models.Transaction) (*big.Int, *big.Int) {
	// Calculate gas costs for front-run and back-run.
	gasCost := big.NewInt(0)
	if front.GasPrice != nil {
		frontGas := new(big.Int).Mul(front.GasPrice, big.NewInt(int64(front.GasUsed)))
		if front.GasUsed == 0 {
			frontGas = new(big.Int).Mul(front.GasPrice, big.NewInt(200_000))
		}
		gasCost.Add(gasCost, frontGas)
	}
	if back.GasPrice != nil {
		backGas := new(big.Int).Mul(back.GasPrice, big.NewInt(int64(back.GasUsed)))
		if back.GasUsed == 0 {
			backGas = new(big.Int).Mul(back.GasPrice, big.NewInt(200_000))
		}
		gasCost.Add(gasCost, backGas)
	}

	// Estimate revenue from the price movement.
	// The revenue is proportional to the victim's trade size and the price impact.
	revenue := big.NewInt(0)
	if victim.Value != nil && victim.Value.Sign() > 0 {
		// Estimate ~0.3% of the victim's trade value as extractable profit
		// (simplified model of constant-product AMM price impact).
		revenue = new(big.Int).Mul(victim.Value, big.NewInt(30))
		revenue.Div(revenue, big.NewInt(10000))
	} else {
		// If value is zero, estimate from gas usage patterns.
		// Higher gas usage often correlates with larger swap amounts.
		estimatedValue := new(big.Int).Mul(big.NewInt(int64(victim.GasUsed)), big.NewInt(1e10))
		revenue = new(big.Int).Mul(estimatedValue, big.NewInt(30))
		revenue.Div(revenue, big.NewInt(10000))
	}

	profit := new(big.Int).Sub(revenue, gasCost)
	return profit, gasCost
}

// evaluateCandidate converts a sandwich candidate into an MEVOpportunity.
func (d *SandwichDetector) evaluateCandidate(candidate SandwichCandidate, block *models.Block) *models.MEVOpportunity {
	revenue := new(big.Int).Add(candidate.Profit, candidate.GasCost)

	return &models.MEVOpportunity{
		ID:       fmt.Sprintf("sandwich-%s-%d", candidate.Victim.Hash[:12], block.Number),
		Strategy: models.StrategySandwich,
		Chain:    block.Chain,
		Transactions: []models.Transaction{
			candidate.FrontRun,
			candidate.Victim,
			candidate.BackRun,
		},
		Revenue:     revenue,
		Cost:        candidate.GasCost,
		Profit:      candidate.Profit,
		BlockNumber: block.Number,
		Timestamp:   block.Timestamp,
		Details: map[string]string{
			"frontrun_tx":  candidate.FrontRun.Hash,
			"victim_tx":    candidate.Victim.Hash,
			"backrun_tx":   candidate.BackRun.Hash,
			"attacker":     candidate.FrontRun.From,
			"victim":       candidate.Victim.From,
			"victim_value": safeIntString(candidate.Victim.Value),
		},
	}
}

// DetectVulnerableTransactions identifies transactions that are vulnerable
// to sandwich attacks due to high slippage tolerance or large trade size.
func (d *SandwichDetector) DetectVulnerableTransactions(block *models.Block) []models.Transaction {
	var vulnerable []models.Transaction

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		if !isSwap(tx) {
			continue
		}

		// Heuristic: transactions with large ETH value are more attractive targets.
		if tx.Value != nil && tx.Value.Cmp(big.NewInt(1e18)) > 0 { // > 1 ETH
			vulnerable = append(vulnerable, *tx)
			continue
		}

		// Heuristic: high gas price indicates urgency and potential slippage tolerance.
		if tx.GasPrice != nil && tx.GasPrice.Cmp(big.NewInt(50e9)) > 0 { // > 50 gwei
			vulnerable = append(vulnerable, *tx)
		}
	}

	return vulnerable
}

// safeIntString returns the string representation of a *big.Int, or "0" if nil.
func safeIntString(n *big.Int) string {
	if n == nil {
		return "0"
	}
	return n.String()
}

// Strategy returns the strategy type.
func (d *SandwichDetector) Strategy() models.StrategyType {
	return models.StrategySandwich
}

// Name returns a human-readable name.
func (d *SandwichDetector) Name() string {
	return "Sandwich Attack Detector"
}
