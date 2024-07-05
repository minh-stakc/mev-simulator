package solana

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"mev_simulator/internal/models"
)

// TransactionParser converts raw Solana RPC data into unified transaction models.
type TransactionParser struct{}

// NewTransactionParser creates a new Solana transaction parser.
func NewTransactionParser() *TransactionParser {
	return &TransactionParser{}
}

// solanaTransaction is the decoded JSON structure of a Solana transaction message.
type solanaTransaction struct {
	Signatures []string `json:"signatures"`
	Message    struct {
		AccountKeys     []string           `json:"accountKeys"`
		RecentBlockhash string             `json:"recentBlockhash"`
		Instructions    []InstructionEntry `json:"instructions"`
	} `json:"message"`
}

// ParseTransaction converts a SlotTransactionResult into a unified Transaction.
func (p *TransactionParser) ParseTransaction(raw SlotTransactionResult, slotNum uint64, index int, blockTime *int64) (models.Transaction, error) {
	tx := models.Transaction{
		Chain:      models.ChainSolana,
		SlotNumber: slotNum,
		Index:      index,
		Value:      big.NewInt(0),
	}

	if blockTime != nil {
		tx.Timestamp = time.Unix(*blockTime, 0)
	}

	// Decode the transaction JSON to extract signatures and account keys.
	var solTx solanaTransaction
	if err := json.Unmarshal(raw.Transaction, &solTx); err != nil {
		return tx, fmt.Errorf("decoding transaction body: %w", err)
	}

	if len(solTx.Signatures) > 0 {
		tx.Hash = solTx.Signatures[0]
	}

	if len(solTx.Message.AccountKeys) > 0 {
		tx.From = solTx.Message.AccountKeys[0] // fee payer
	}
	if len(solTx.Message.AccountKeys) > 1 {
		tx.To = solTx.Message.AccountKeys[1]
	}

	// Fill in metadata fields.
	if raw.Meta != nil {
		tx.GasPrice = big.NewInt(int64(raw.Meta.Fee))
		tx.GasUsed = raw.Meta.Fee

		if raw.Meta.Err == nil {
			tx.Status = models.TxStatusSuccess
		} else {
			tx.Status = models.TxStatusFailed
		}

		// Compute the net SOL transfer (change in lamports for the fee payer).
		if len(raw.Meta.PreBalances) > 0 && len(raw.Meta.PostBalances) > 0 {
			pre := raw.Meta.PreBalances[0]
			post := raw.Meta.PostBalances[0]
			if pre > post {
				tx.Value = big.NewInt(int64(pre - post - raw.Meta.Fee))
				if tx.Value.Sign() < 0 {
					tx.Value = big.NewInt(0)
				}
			}
		}

		// Convert log messages into LogEntry items.
		for _, msg := range raw.Meta.LogMessages {
			tx.Logs = append(tx.Logs, models.LogEntry{
				Data: []byte(msg),
			})
		}
	}

	return tx, nil
}

// ParseSlotTransactions parses all transactions from a slot result.
func (p *TransactionParser) ParseSlotTransactions(slot *SlotResult, slotNum uint64) ([]models.Transaction, error) {
	txs := make([]models.Transaction, 0, len(slot.Transactions))
	for i, raw := range slot.Transactions {
		tx, err := p.ParseTransaction(raw, slotNum, i, slot.BlockTime)
		if err != nil {
			return nil, fmt.Errorf("parsing tx index %d in slot %d: %w", i, slotNum, err)
		}
		txs = append(txs, tx)
	}
	return txs, nil
}

// IsDEXSwap checks if a Solana transaction interacts with known DEX programs.
func IsDEXSwap(tx *models.Transaction) bool {
	knownDEXPrograms := map[string]bool{
		"675kPX9MHTjS2zt1qfr1NYHuzeLXfQM9H24wFSUt1Mp8": true, // Raydium AMM
		"whirLbMiicVdio4qvUfM5KAg6Ct8VwpYzGff3uctyCc":  true, // Orca Whirlpool
		"JUP6LkbZbjS1jKKwapdHNy74zcZ3tLUZoi5QNyVTaV4":  true, // Jupiter v6
		"9W959DqEETiGZocYWCQPaJ6sBmUzgfxXfqGeTEdp3aQP": true, // Orca Token Swap
		"srmqPvymJeFKQ4zGQed1GFppgkRHL9kaELCbyksJtPX":  true, // Serum DEX
	}

	// Check if the To field matches a known DEX program.
	if knownDEXPrograms[tx.To] {
		return true
	}

	// Also check log messages for program invocations.
	for _, log := range tx.Logs {
		msg := string(log.Data)
		for prog := range knownDEXPrograms {
			if len(msg) > 0 && containsProgram(msg, prog) {
				return true
			}
		}
	}

	return false
}

// containsProgram checks if a log message references a program ID.
func containsProgram(logMsg, programID string) bool {
	// Solana logs "Program <id> invoke" for CPI calls.
	return len(logMsg) > len(programID) && indexOf(logMsg, programID) >= 0
}

// indexOf is a simple substring search.
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// ExtractTokenChanges computes the net token balance changes from a transaction.
func ExtractTokenChanges(meta *TransactionMeta) map[string]map[string]int64 {
	// Returns map[mint]map[owner]delta
	changes := make(map[string]map[string]int64)
	if meta == nil {
		return changes
	}

	// Index pre-balances by (accountIndex).
	preMap := make(map[int]TokenBalance)
	for _, tb := range meta.PreTokenBalances {
		preMap[tb.AccountIndex] = tb
	}

	for _, post := range meta.PostTokenBalances {
		pre, hasPre := preMap[post.AccountIndex]
		if !hasPre {
			continue
		}

		preAmt := new(big.Int)
		preAmt.SetString(pre.UITokenAmount.Amount, 10)

		postAmt := new(big.Int)
		postAmt.SetString(post.UITokenAmount.Amount, 10)

		delta := new(big.Int).Sub(postAmt, preAmt)
		if delta.Sign() == 0 {
			continue
		}

		mint := post.Mint
		owner := post.Owner
		if changes[mint] == nil {
			changes[mint] = make(map[string]int64)
		}
		changes[mint][owner] += delta.Int64()
	}

	return changes
}
