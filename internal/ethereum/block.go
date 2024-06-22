package ethereum

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"time"

	"mev_simulator/internal/models"
)

// BlockAnalyzer provides methods for analyzing Ethereum blocks and their
// transaction ordering patterns.
type BlockAnalyzer struct {
	client *Client
	parser *TransactionParser
}

// NewBlockAnalyzer creates a block analyzer with the given client.
func NewBlockAnalyzer(client *Client) *BlockAnalyzer {
	return &BlockAnalyzer{
		client: client,
		parser: NewTransactionParser(client),
	}
}

// FetchBlock retrieves and parses a block into the unified model.
func (a *BlockAnalyzer) FetchBlock(ctx context.Context, blockNum uint64) (*models.Block, error) {
	raw, err := a.client.GetBlockByNumber(ctx, blockNum)
	if err != nil {
		return nil, err
	}

	txs, err := a.parser.ParseBlockTransactions(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing transactions in block %d: %w", blockNum, err)
	}

	block := &models.Block{
		Chain:        models.ChainEthereum,
		Hash:         raw.Hash,
		ParentHash:   raw.ParentHash,
		Miner:        raw.Miner,
		Transactions: txs,
	}

	if bn, ok := parseUint64Hex(raw.Number); ok {
		block.Number = bn
	}
	if ts, ok := parseUint64Hex(raw.Timestamp); ok {
		block.Timestamp = time.Unix(int64(ts), 0)
	}
	if gu, ok := parseUint64Hex(raw.GasUsed); ok {
		block.GasUsed = gu
	}
	if gl, ok := parseUint64Hex(raw.GasLimit); ok {
		block.GasLimit = gl
	}
	if bf, ok := parseBigHex(raw.BaseFeePerGas); ok {
		block.BaseFee = bf
	}

	return block, nil
}

// FetchBlockRange retrieves a sequential range of blocks.
func (a *BlockAnalyzer) FetchBlockRange(ctx context.Context, start, end uint64) ([]*models.Block, error) {
	if end < start {
		return nil, fmt.Errorf("invalid range: end %d < start %d", end, start)
	}

	blocks := make([]*models.Block, 0, end-start+1)
	for num := start; num <= end; num++ {
		select {
		case <-ctx.Done():
			return blocks, ctx.Err()
		default:
		}

		block, err := a.FetchBlock(ctx, num)
		if err != nil {
			return blocks, fmt.Errorf("fetching block %d: %w", num, err)
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// OrderingAnalysis describes how transactions were ordered in a block.
type OrderingAnalysis struct {
	BlockNumber       uint64
	TotalTransactions int
	GasPriceOrdered   bool
	HighestGasPrice   *big.Int
	LowestGasPrice    *big.Int
	MedianGasPrice    *big.Int
	PriorityFeeTips   []*big.Int
	SwapCount         int
	MinerTxCount      int // transactions sent by the block's miner/builder
}

// AnalyzeOrdering examines the transaction ordering within a block to detect
// whether transactions are sorted by gas price (typical for priority ordering)
// and identifies patterns like miner-inserted transactions.
func (a *BlockAnalyzer) AnalyzeOrdering(block *models.Block) OrderingAnalysis {
	analysis := OrderingAnalysis{
		BlockNumber:       block.Number,
		TotalTransactions: len(block.Transactions),
	}

	if len(block.Transactions) == 0 {
		return analysis
	}

	// Collect gas prices and check ordering.
	gasPrices := make([]*big.Int, 0, len(block.Transactions))
	for i := range block.Transactions {
		tx := &block.Transactions[i]
		gp := tx.GasPrice
		if gp == nil {
			gp = big.NewInt(0)
		}
		gasPrices = append(gasPrices, gp)

		if IsSwapTransaction(tx) {
			analysis.SwapCount++
		}
		if tx.From == block.Miner {
			analysis.MinerTxCount++
		}
	}

	// Check if transactions are ordered by descending gas price.
	ordered := true
	for i := 1; i < len(gasPrices); i++ {
		if gasPrices[i].Cmp(gasPrices[i-1]) > 0 {
			ordered = false
			break
		}
	}
	analysis.GasPriceOrdered = ordered

	// Compute price statistics.
	sorted := make([]*big.Int, len(gasPrices))
	copy(sorted, gasPrices)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Cmp(sorted[j]) < 0
	})

	analysis.LowestGasPrice = sorted[0]
	analysis.HighestGasPrice = sorted[len(sorted)-1]
	analysis.MedianGasPrice = sorted[len(sorted)/2]

	// Compute priority fee tips (gas price - base fee) when base fee is available.
	if block.BaseFee != nil && block.BaseFee.Sign() > 0 {
		for _, gp := range gasPrices {
			tip := new(big.Int).Sub(gp, block.BaseFee)
			if tip.Sign() < 0 {
				tip = big.NewInt(0)
			}
			analysis.PriorityFeeTips = append(analysis.PriorityFeeTips, tip)
		}
	}

	return analysis
}

// DetectBundledTransactions identifies groups of consecutive transactions that
// appear to be part of a MEV bundle (e.g., same sender, or a sandwich pattern).
func (a *BlockAnalyzer) DetectBundledTransactions(block *models.Block) [][]int {
	if len(block.Transactions) < 2 {
		return nil
	}

	var bundles [][]int

	// Detect same-sender consecutive sequences (potential bundles).
	i := 0
	for i < len(block.Transactions) {
		j := i + 1
		for j < len(block.Transactions) && block.Transactions[j].From == block.Transactions[i].From {
			j++
		}
		if j-i >= 2 {
			indices := make([]int, 0, j-i)
			for k := i; k < j; k++ {
				indices = append(indices, k)
			}
			bundles = append(bundles, indices)
		}
		i = j
	}

	// Detect sandwich patterns: swap, victim_swap, swap from same sender
	// where positions i and i+2 share a sender but i+1 does not.
	for i := 0; i+2 < len(block.Transactions); i++ {
		tx0 := &block.Transactions[i]
		tx1 := &block.Transactions[i+1]
		tx2 := &block.Transactions[i+2]

		if IsSwapTransaction(tx0) && IsSwapTransaction(tx1) && IsSwapTransaction(tx2) {
			if tx0.From == tx2.From && tx0.From != tx1.From {
				bundles = append(bundles, []int{i, i + 1, i + 2})
			}
		}
	}

	return bundles
}
