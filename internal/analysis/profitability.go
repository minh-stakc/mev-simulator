package analysis

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	"mev_simulator/internal/models"
)

// ProfitabilityAnalyzer computes ROI, profit distributions, and strategy
// performance metrics from simulation results.
type ProfitabilityAnalyzer struct {
	ethPriceUSD  *big.Float // ETH price in USD for reporting
	solPriceUSD  *big.Float // SOL price in USD for reporting
}

// NewProfitabilityAnalyzer creates an analyzer with reference prices.
func NewProfitabilityAnalyzer(ethPriceUSD, solPriceUSD float64) *ProfitabilityAnalyzer {
	return &ProfitabilityAnalyzer{
		ethPriceUSD: big.NewFloat(ethPriceUSD),
		solPriceUSD: big.NewFloat(solPriceUSD),
	}
}

// ProfitReport is the complete profitability analysis output.
type ProfitReport struct {
	TotalOpportunities int
	ProfitableCount    int
	UnprofitableCount  int
	TotalRevenueWei    *big.Int
	TotalCostWei       *big.Int
	TotalProfitWei     *big.Int
	TotalRevenueUSD    *big.Float
	TotalCostUSD       *big.Float
	TotalProfitUSD     *big.Float
	ROI                float64
	StrategyReports    map[models.StrategyType]*StrategyReport
	TopOpportunities   []OpportunitySummary
	ChainBreakdown     map[models.Chain]*ChainReport
}

// StrategyReport holds performance metrics for a single strategy.
type StrategyReport struct {
	Strategy    models.StrategyType
	Count       int
	Profitable  int
	Revenue     *big.Int
	Cost        *big.Int
	Profit      *big.Int
	AvgProfit   *big.Int
	MaxProfit   *big.Int
	MinProfit   *big.Int
	ROI         float64
	SuccessRate float64
}

// ChainReport holds per-chain breakdown.
type ChainReport struct {
	Chain           models.Chain
	Opportunities   int
	TotalProfit     *big.Int
	TotalProfitUSD  *big.Float
}

// OpportunitySummary is a condensed view of an opportunity for reporting.
type OpportunitySummary struct {
	ID       string
	Strategy models.StrategyType
	Chain    models.Chain
	Profit   *big.Int
	Block    uint64
}

// Analyze generates a complete profitability report from simulation results.
func (a *ProfitabilityAnalyzer) Analyze(result *models.SimulationResult) *ProfitReport {
	report := &ProfitReport{
		TotalOpportunities: len(result.Opportunities),
		TotalRevenueWei:    big.NewInt(0),
		TotalCostWei:       big.NewInt(0),
		TotalProfitWei:     big.NewInt(0),
		StrategyReports:    make(map[models.StrategyType]*StrategyReport),
		ChainBreakdown:     make(map[models.Chain]*ChainReport),
	}

	for i := range result.Opportunities {
		opp := &result.Opportunities[i]
		a.processOpportunity(report, opp)
	}

	// Finalize strategy reports.
	for _, sr := range report.StrategyReports {
		if sr.Count > 0 {
			sr.AvgProfit = new(big.Int).Div(sr.Profit, big.NewInt(int64(sr.Count)))
			sr.SuccessRate = float64(sr.Profitable) / float64(sr.Count)
			if sr.Cost.Sign() > 0 {
				profitF := new(big.Float).SetInt(sr.Profit)
				costF := new(big.Float).SetInt(sr.Cost)
				roiF := new(big.Float).Quo(profitF, costF)
				sr.ROI, _ = roiF.Float64()
				sr.ROI *= 100.0
			}
		}
	}

	// Compute USD values.
	report.TotalRevenueUSD = a.weiToUSD(report.TotalRevenueWei, models.ChainEthereum)
	report.TotalCostUSD = a.weiToUSD(report.TotalCostWei, models.ChainEthereum)
	report.TotalProfitUSD = a.weiToUSD(report.TotalProfitWei, models.ChainEthereum)

	// Compute overall ROI.
	if report.TotalCostWei.Sign() > 0 {
		profitF := new(big.Float).SetInt(report.TotalProfitWei)
		costF := new(big.Float).SetInt(report.TotalCostWei)
		roiF := new(big.Float).Quo(profitF, costF)
		report.ROI, _ = roiF.Float64()
		report.ROI *= 100.0
	}

	// Chain breakdowns USD conversion.
	for chain, cr := range report.ChainBreakdown {
		cr.TotalProfitUSD = a.weiToUSD(cr.TotalProfit, chain)
	}

	// Top opportunities by profit.
	report.TopOpportunities = a.topOpportunities(result.Opportunities, 10)

	return report
}

// processOpportunity accumulates a single opportunity into the report.
func (a *ProfitabilityAnalyzer) processOpportunity(report *ProfitReport, opp *models.MEVOpportunity) {
	if opp.IsProfitable() {
		report.ProfitableCount++
	} else {
		report.UnprofitableCount++
	}

	if opp.Revenue != nil {
		report.TotalRevenueWei.Add(report.TotalRevenueWei, opp.Revenue)
	}
	if opp.Cost != nil {
		report.TotalCostWei.Add(report.TotalCostWei, opp.Cost)
	}
	if opp.Profit != nil {
		report.TotalProfitWei.Add(report.TotalProfitWei, opp.Profit)
	}

	// Strategy breakdown.
	sr, ok := report.StrategyReports[opp.Strategy]
	if !ok {
		sr = &StrategyReport{
			Strategy: opp.Strategy,
			Revenue:  big.NewInt(0),
			Cost:     big.NewInt(0),
			Profit:   big.NewInt(0),
		}
		report.StrategyReports[opp.Strategy] = sr
	}
	sr.Count++
	if opp.IsProfitable() {
		sr.Profitable++
	}
	if opp.Revenue != nil {
		sr.Revenue.Add(sr.Revenue, opp.Revenue)
	}
	if opp.Cost != nil {
		sr.Cost.Add(sr.Cost, opp.Cost)
	}
	if opp.Profit != nil {
		sr.Profit.Add(sr.Profit, opp.Profit)
		if sr.MaxProfit == nil || opp.Profit.Cmp(sr.MaxProfit) > 0 {
			sr.MaxProfit = new(big.Int).Set(opp.Profit)
		}
		if sr.MinProfit == nil || opp.Profit.Cmp(sr.MinProfit) < 0 {
			sr.MinProfit = new(big.Int).Set(opp.Profit)
		}
	}

	// Chain breakdown.
	cr, ok := report.ChainBreakdown[opp.Chain]
	if !ok {
		cr = &ChainReport{
			Chain:       opp.Chain,
			TotalProfit: big.NewInt(0),
		}
		report.ChainBreakdown[opp.Chain] = cr
	}
	cr.Opportunities++
	if opp.Profit != nil {
		cr.TotalProfit.Add(cr.TotalProfit, opp.Profit)
	}
}

// weiToUSD converts a wei or lamport amount to USD.
func (a *ProfitabilityAnalyzer) weiToUSD(amount *big.Int, chain models.Chain) *big.Float {
	if amount == nil {
		return big.NewFloat(0)
	}

	amountF := new(big.Float).SetInt(amount)

	switch chain {
	case models.ChainEthereum:
		// Wei to ETH: divide by 1e18, then multiply by ETH price.
		ethAmount := new(big.Float).Quo(amountF, big.NewFloat(1e18))
		return new(big.Float).Mul(ethAmount, a.ethPriceUSD)
	case models.ChainSolana:
		// Lamports to SOL: divide by 1e9, then multiply by SOL price.
		solAmount := new(big.Float).Quo(amountF, big.NewFloat(1e9))
		return new(big.Float).Mul(solAmount, a.solPriceUSD)
	default:
		return big.NewFloat(0)
	}
}

// topOpportunities returns the top N opportunities sorted by profit descending.
func (a *ProfitabilityAnalyzer) topOpportunities(opps []models.MEVOpportunity, n int) []OpportunitySummary {
	type indexed struct {
		idx    int
		profit *big.Int
	}

	items := make([]indexed, 0, len(opps))
	for i := range opps {
		p := opps[i].Profit
		if p == nil {
			p = big.NewInt(0)
		}
		items = append(items, indexed{idx: i, profit: p})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].profit.Cmp(items[j].profit) > 0
	})

	if n > len(items) {
		n = len(items)
	}

	summaries := make([]OpportunitySummary, n)
	for i := 0; i < n; i++ {
		opp := &opps[items[i].idx]
		summaries[i] = OpportunitySummary{
			ID:       opp.ID,
			Strategy: opp.Strategy,
			Chain:    opp.Chain,
			Profit:   opp.Profit,
			Block:    opp.BlockNumber,
		}
	}

	return summaries
}

// FormatReport produces a human-readable text summary of the profitability report.
func FormatReport(report *ProfitReport) string {
	var b strings.Builder

	b.WriteString("=== MEV Simulation Profitability Report ===\n\n")
	b.WriteString(fmt.Sprintf("Total Opportunities:  %d\n", report.TotalOpportunities))
	b.WriteString(fmt.Sprintf("Profitable:           %d\n", report.ProfitableCount))
	b.WriteString(fmt.Sprintf("Unprofitable:         %d\n", report.UnprofitableCount))
	b.WriteString(fmt.Sprintf("Total Revenue (wei):  %s\n", report.TotalRevenueWei.String()))
	b.WriteString(fmt.Sprintf("Total Cost (wei):     %s\n", report.TotalCostWei.String()))
	b.WriteString(fmt.Sprintf("Total Profit (wei):   %s\n", report.TotalProfitWei.String()))

	if report.TotalProfitUSD != nil {
		usd, _ := report.TotalProfitUSD.Float64()
		b.WriteString(fmt.Sprintf("Total Profit (USD):   $%.2f\n", usd))
	}
	b.WriteString(fmt.Sprintf("ROI:                  %.2f%%\n", report.ROI))

	b.WriteString("\n--- Strategy Breakdown ---\n")
	for _, sr := range report.StrategyReports {
		b.WriteString(fmt.Sprintf("\n[%s]\n", sr.Strategy))
		b.WriteString(fmt.Sprintf("  Count:        %d\n", sr.Count))
		b.WriteString(fmt.Sprintf("  Profitable:   %d (%.1f%%)\n", sr.Profitable, sr.SuccessRate*100))
		b.WriteString(fmt.Sprintf("  Total Profit: %s wei\n", sr.Profit.String()))
		b.WriteString(fmt.Sprintf("  Avg Profit:   %s wei\n", sr.AvgProfit.String()))
		b.WriteString(fmt.Sprintf("  ROI:          %.2f%%\n", sr.ROI))
		if sr.MaxProfit != nil {
			b.WriteString(fmt.Sprintf("  Max Profit:   %s wei\n", sr.MaxProfit.String()))
		}
	}

	b.WriteString("\n--- Chain Breakdown ---\n")
	for _, cr := range report.ChainBreakdown {
		usd := float64(0)
		if cr.TotalProfitUSD != nil {
			usd, _ = cr.TotalProfitUSD.Float64()
		}
		b.WriteString(fmt.Sprintf("\n[%s]\n", cr.Chain))
		b.WriteString(fmt.Sprintf("  Opportunities: %d\n", cr.Opportunities))
		b.WriteString(fmt.Sprintf("  Total Profit:  %s wei ($%.2f)\n", cr.TotalProfit.String(), usd))
	}

	if len(report.TopOpportunities) > 0 {
		b.WriteString("\n--- Top Opportunities ---\n")
		for i, top := range report.TopOpportunities {
			b.WriteString(fmt.Sprintf("%d. [%s] %s | Block %d | Profit: %s wei\n",
				i+1, top.Strategy, top.Chain, top.Block, top.Profit.String()))
		}
	}

	return b.String()
}
