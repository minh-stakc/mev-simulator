package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"

	"mev_simulator/internal/models"
)

// TraceAnalyzer processes transaction execution traces to study internal
// call patterns, value flows, and block inclusion ordering.
type TraceAnalyzer struct {
	maxDepth int
}

// NewTraceAnalyzer creates a trace analyzer with the given maximum recursion depth.
func NewTraceAnalyzer(maxDepth int) *TraceAnalyzer {
	if maxDepth <= 0 {
		maxDepth = 10
	}
	return &TraceAnalyzer{maxDepth: maxDepth}
}

// EthTracer defines the interface for fetching Ethereum traces.
type EthTracer interface {
	TraceTransaction(ctx context.Context, txHash string) (json.RawMessage, error)
}

// rawTrace is the structure returned by the callTracer.
type rawTrace struct {
	Type    string     `json:"type"`
	From    string     `json:"from"`
	To      string     `json:"to"`
	Value   string     `json:"value"`
	Gas     string     `json:"gas"`
	GasUsed string     `json:"gasUsed"`
	Input   string     `json:"input"`
	Output  string     `json:"output"`
	Error   string     `json:"error"`
	Calls   []rawTrace `json:"calls"`
}

// FetchAndParseTrace retrieves the execution trace for an Ethereum transaction
// and converts it into the unified TransactionTrace model.
func (a *TraceAnalyzer) FetchAndParseTrace(ctx context.Context, tracer EthTracer, txHash string) (*models.TransactionTrace, error) {
	raw, err := tracer.TraceTransaction(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("fetching trace for %s: %w", txHash, err)
	}

	var rt rawTrace
	if err := json.Unmarshal(raw, &rt); err != nil {
		return nil, fmt.Errorf("parsing trace for %s: %w", txHash, err)
	}

	trace := &models.TransactionTrace{
		TxHash: txHash,
		Chain:  models.ChainEthereum,
	}

	a.flattenCalls(&rt, trace, 0)

	// Compute aggregate stats.
	var totalGas uint64
	maxDepth := 0
	for _, call := range trace.Calls {
		totalGas += call.GasUsed
		if call.Depth > maxDepth {
			maxDepth = call.Depth
		}
		if call.Error != "" {
			trace.Reverted = true
		}
	}
	trace.GasUsed = totalGas
	trace.Depth = maxDepth

	return trace, nil
}

// flattenCalls recursively converts a nested rawTrace into a flat list of TraceCall.
func (a *TraceAnalyzer) flattenCalls(rt *rawTrace, trace *models.TransactionTrace, depth int) {
	if depth > a.maxDepth {
		return
	}

	call := models.TraceCall{
		Type:  rt.Type,
		From:  rt.From,
		To:    rt.To,
		Input: decodeHex(rt.Input),
		Depth: depth,
		Error: rt.Error,
	}

	if v, ok := parseBigHex(rt.Value); ok {
		call.Value = v
	}
	if g, ok := parseUint64Hex(rt.GasUsed); ok {
		call.GasUsed = g
	}
	if rt.Output != "" {
		call.Output = decodeHex(rt.Output)
	}

	trace.Calls = append(trace.Calls, call)

	for i := range rt.Calls {
		a.flattenCalls(&rt.Calls[i], trace, depth+1)
	}
}

// ValueFlow describes how value (ETH/SOL) flows through internal calls.
type ValueFlow struct {
	From   string
	To     string
	Value  *big.Int
	Depth  int
	CallType string
}

// AnalyzeValueFlows extracts all value transfers from a transaction trace.
func (a *TraceAnalyzer) AnalyzeValueFlows(trace *models.TransactionTrace) []ValueFlow {
	var flows []ValueFlow

	for _, call := range trace.Calls {
		if call.Value != nil && call.Value.Sign() > 0 {
			flows = append(flows, ValueFlow{
				From:     call.From,
				To:       call.To,
				Value:    call.Value,
				Depth:    call.Depth,
				CallType: call.Type,
			})
		}
	}

	return flows
}

// BlockOrderingPattern describes the transaction ordering pattern in a block.
type BlockOrderingPattern struct {
	BlockNumber       uint64
	TotalTransactions int
	OrderingType      string // "priority_fee", "first_come", "builder_ordered", "unknown"
	BundleCount       int
	PrivateTxCount    int // transactions not seen in the public mempool
	GasPriceSpread    *big.Int
}

// AnalyzeBlockOrdering examines a block's transactions to determine the
// ordering strategy used by the block builder/validator.
func (a *TraceAnalyzer) AnalyzeBlockOrdering(block *models.Block) BlockOrderingPattern {
	pattern := BlockOrderingPattern{
		BlockNumber:       block.Number,
		TotalTransactions: len(block.Transactions),
	}

	if len(block.Transactions) == 0 {
		pattern.OrderingType = "empty"
		return pattern
	}

	// Collect effective gas prices.
	gasPrices := make([]*big.Int, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		gp := tx.GasPrice
		if gp == nil {
			gp = big.NewInt(0)
		}
		gasPrices = append(gasPrices, gp)
	}

	// Check for descending priority fee ordering.
	descending := true
	for i := 1; i < len(gasPrices); i++ {
		if gasPrices[i].Cmp(gasPrices[i-1]) > 0 {
			descending = false
			break
		}
	}

	if descending {
		pattern.OrderingType = "priority_fee"
	} else {
		// Check if ordering looks builder-optimized (non-monotonic but with bundles).
		pattern.OrderingType = a.classifyOrdering(gasPrices, block)
	}

	// Compute gas price spread.
	sorted := make([]*big.Int, len(gasPrices))
	copy(sorted, gasPrices)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Cmp(sorted[j]) < 0
	})
	pattern.GasPriceSpread = new(big.Int).Sub(sorted[len(sorted)-1], sorted[0])

	// Count potential bundles (consecutive same-sender transactions).
	seen := make(map[string]int)
	for _, tx := range block.Transactions {
		seen[tx.From]++
	}
	for _, count := range seen {
		if count >= 2 {
			pattern.BundleCount++
		}
	}

	return pattern
}

// classifyOrdering determines the ordering strategy when it's not simple priority fee.
func (a *TraceAnalyzer) classifyOrdering(gasPrices []*big.Int, block *models.Block) string {
	// Check if the block has miner/builder-injected transactions at the start.
	if len(block.Transactions) > 0 && block.Transactions[0].From == block.Miner {
		return "builder_ordered"
	}

	// Check for interleaved high/low gas prices (typical of bundle insertion).
	inversions := 0
	for i := 1; i < len(gasPrices); i++ {
		if gasPrices[i].Cmp(gasPrices[i-1]) > 0 {
			inversions++
		}
	}

	inversionRate := float64(inversions) / float64(len(gasPrices)-1)
	if inversionRate > 0.3 {
		return "builder_ordered"
	}

	return "unknown"
}

// AnalyzeTraceDepthDistribution computes the distribution of call depths
// across a set of transaction traces.
func (a *TraceAnalyzer) AnalyzeTraceDepthDistribution(traces []*models.TransactionTrace) map[int]int {
	distribution := make(map[int]int)
	for _, trace := range traces {
		for _, call := range trace.Calls {
			distribution[call.Depth]++
		}
	}
	return distribution
}

// IdentifyHighValueTraces returns traces where the total value transferred
// exceeds the given threshold.
func (a *TraceAnalyzer) IdentifyHighValueTraces(traces []*models.TransactionTrace, threshold *big.Int) []*models.TransactionTrace {
	var highValue []*models.TransactionTrace

	for _, trace := range traces {
		totalValue := big.NewInt(0)
		for _, call := range trace.Calls {
			if call.Value != nil {
				totalValue.Add(totalValue, call.Value)
			}
		}

		if totalValue.Cmp(threshold) >= 0 {
			highValue = append(highValue, trace)
		}
	}

	return highValue
}

// Helper functions shared with ethereum package conventions.

func parseBigHex(s string) (*big.Int, bool) {
	if len(s) > 2 && s[:2] == "0x" {
		s = s[2:]
	}
	if s == "" {
		return big.NewInt(0), true
	}
	n := new(big.Int)
	_, ok := n.SetString(s, 16)
	return n, ok
}

func parseUint64Hex(s string) (uint64, bool) {
	n, ok := parseBigHex(s)
	if !ok {
		return 0, false
	}
	return n.Uint64(), true
}

func decodeHex(s string) []byte {
	if len(s) > 2 && s[:2] == "0x" {
		s = s[2:]
	}
	if s == "" || len(s)%2 != 0 {
		return nil
	}
	data := make([]byte, len(s)/2)
	for i := 0; i < len(data); i++ {
		high := unhex(s[2*i])
		low := unhex(s[2*i+1])
		if high == 0xFF || low == 0xFF {
			return nil
		}
		data[i] = high<<4 | low
	}
	return data
}

func unhex(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10
	default:
		return 0xFF
	}
}
