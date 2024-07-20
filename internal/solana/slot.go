package solana

import (
	"context"
	"fmt"
	"time"

	"mev_simulator/internal/models"
)

// SlotAnalyzer provides methods for analyzing Solana slots and leader schedules.
type SlotAnalyzer struct {
	client *Client
	parser *TransactionParser
}

// NewSlotAnalyzer creates a new slot analyzer backed by the given client.
func NewSlotAnalyzer(client *Client) *SlotAnalyzer {
	return &SlotAnalyzer{
		client: client,
		parser: NewTransactionParser(),
	}
}

// FetchSlot retrieves and parses a Solana slot into the unified Block model.
func (a *SlotAnalyzer) FetchSlot(ctx context.Context, slotNum uint64) (*models.Block, error) {
	raw, err := a.client.GetSlot(ctx, slotNum)
	if err != nil {
		return nil, err
	}

	txs, err := a.parser.ParseSlotTransactions(raw, slotNum)
	if err != nil {
		return nil, fmt.Errorf("parsing transactions in slot %d: %w", slotNum, err)
	}

	block := &models.Block{
		Chain:        models.ChainSolana,
		Number:       slotNum,
		Hash:         raw.Blockhash,
		ParentHash:   raw.PreviousBlockhash,
		Transactions: txs,
	}

	if raw.BlockTime != nil {
		block.Timestamp = time.Unix(*raw.BlockTime, 0)
	}

	return block, nil
}

// FetchSlotRange retrieves a range of Solana slots. Skipped slots are ignored.
func (a *SlotAnalyzer) FetchSlotRange(ctx context.Context, start, end uint64) ([]*models.Block, error) {
	if end < start {
		return nil, fmt.Errorf("invalid range: end %d < start %d", end, start)
	}

	blocks := make([]*models.Block, 0, end-start+1)
	for slot := start; slot <= end; slot++ {
		select {
		case <-ctx.Done():
			return blocks, ctx.Err()
		default:
		}

		block, err := a.FetchSlot(ctx, slot)
		if err != nil {
			// Solana has skipped slots; log and continue.
			continue
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// LeaderAnalysis describes the leader and transaction patterns for a slot.
type LeaderAnalysis struct {
	SlotNumber        uint64
	Leader            string
	TotalTransactions int
	SuccessCount      int
	FailedCount       int
	TotalFees         uint64
	DEXSwapCount      int
	AvgFeePerTx       uint64
}

// AnalyzeSlot examines a slot's transactions for leader behavior and MEV patterns.
func (a *SlotAnalyzer) AnalyzeSlot(block *models.Block) LeaderAnalysis {
	analysis := LeaderAnalysis{
		SlotNumber:        block.Number,
		Leader:            block.Miner,
		TotalTransactions: len(block.Transactions),
	}

	var totalFees uint64
	for i := range block.Transactions {
		tx := &block.Transactions[i]
		if tx.Status == models.TxStatusSuccess {
			analysis.SuccessCount++
		} else if tx.Status == models.TxStatusFailed {
			analysis.FailedCount++
		}

		if tx.GasPrice != nil {
			totalFees += tx.GasPrice.Uint64()
		}

		if IsDEXSwap(tx) {
			analysis.DEXSwapCount++
		}
	}

	analysis.TotalFees = totalFees
	if analysis.TotalTransactions > 0 {
		analysis.AvgFeePerTx = totalFees / uint64(analysis.TotalTransactions)
	}

	return analysis
}

// LeaderScheduleInfo holds the resolved leader assignment for a slot range.
type LeaderScheduleInfo struct {
	SlotStart uint64
	SlotEnd   uint64
	Leaders   map[uint64]string // slot -> leader pubkey
}

// ResolveLeaderSchedule maps each slot in a range to its assigned leader.
func (a *SlotAnalyzer) ResolveLeaderSchedule(ctx context.Context, start, end uint64) (*LeaderScheduleInfo, error) {
	schedule, err := a.client.GetLeaderSchedule(ctx, start)
	if err != nil {
		return nil, fmt.Errorf("fetching leader schedule at slot %d: %w", start, err)
	}

	info := &LeaderScheduleInfo{
		SlotStart: start,
		SlotEnd:   end,
		Leaders:   make(map[uint64]string),
	}

	// Invert the schedule: validator -> slots into slot -> validator.
	for validator, slots := range schedule {
		for _, s := range slots {
			absoluteSlot := start + s
			if absoluteSlot >= start && absoluteSlot <= end {
				info.Leaders[absoluteSlot] = validator
			}
		}
	}

	return info, nil
}

// DetectJitoBundle looks for patterns consistent with Jito-style MEV bundles
// in a Solana slot: consecutive transactions from the same fee payer with
// high fees that include DEX interactions.
func (a *SlotAnalyzer) DetectJitoBundle(block *models.Block) [][]int {
	if len(block.Transactions) < 2 {
		return nil
	}

	var bundles [][]int

	i := 0
	for i < len(block.Transactions) {
		tx := &block.Transactions[i]

		// Look for high-fee DEX transactions as bundle candidates.
		if !IsDEXSwap(tx) {
			i++
			continue
		}

		bundle := []int{i}
		j := i + 1
		for j < len(block.Transactions) {
			next := &block.Transactions[j]
			// Bundle heuristic: consecutive transactions from the same sender,
			// or transactions that interact with the same program.
			if next.From == tx.From || IsDEXSwap(next) {
				bundle = append(bundle, j)
				j++
				if len(bundle) >= 5 {
					break
				}
			} else {
				break
			}
		}

		if len(bundle) >= 2 {
			bundles = append(bundles, bundle)
		}
		i = j
	}

	return bundles
}
