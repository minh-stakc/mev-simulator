package ethereum

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"mev_simulator/internal/models"
)

// TransactionParser converts raw Ethereum RPC data into unified transaction models.
type TransactionParser struct {
	client *Client
}

// NewTransactionParser creates a parser backed by the given RPC client.
func NewTransactionParser(client *Client) *TransactionParser {
	return &TransactionParser{client: client}
}

// ParseRawTransaction converts a raw ethTxResult into a models.Transaction.
func (p *TransactionParser) ParseRawTransaction(raw ethTxResult) (models.Transaction, error) {
	tx := models.Transaction{
		Hash:  raw.Hash,
		Chain: models.ChainEthereum,
		From:  raw.From,
		To:    raw.To,
	}

	if v, ok := parseBigHex(raw.Value); ok {
		tx.Value = v
	} else {
		tx.Value = big.NewInt(0)
	}

	if gp, ok := parseBigHex(raw.GasPrice); ok {
		tx.GasPrice = gp
	}

	if n, ok := parseUint64Hex(raw.Nonce); ok {
		tx.Nonce = n
	}

	if idx, ok := parseUint64Hex(raw.TransactionIndex); ok {
		tx.Index = int(idx)
	}

	tx.Input = decodeHexData(raw.Input)

	return tx, nil
}

// EnrichWithReceipt fetches the transaction receipt and fills in status, gas used, and logs.
func (p *TransactionParser) EnrichWithReceipt(ctx context.Context, tx *models.Transaction) error {
	receipt, err := p.client.GetTransactionReceipt(ctx, tx.Hash)
	if err != nil {
		return fmt.Errorf("fetching receipt for %s: %w", tx.Hash, err)
	}

	if receipt.Status == "0x1" {
		tx.Status = models.TxStatusSuccess
	} else {
		tx.Status = models.TxStatusFailed
	}

	if gu, ok := parseUint64Hex(receipt.GasUsed); ok {
		tx.GasUsed = gu
	}

	tx.Logs = make([]models.LogEntry, 0, len(receipt.Logs))
	for _, l := range receipt.Logs {
		tx.Logs = append(tx.Logs, models.LogEntry{
			Address: l.Address,
			Topics:  l.Topics,
			Data:    decodeHexData(l.Data),
		})
	}

	return nil
}

// ParseBlockTransactions converts all transactions from a raw block result.
func (p *TransactionParser) ParseBlockTransactions(block *ethBlockResult) ([]models.Transaction, error) {
	txs := make([]models.Transaction, 0, len(block.Transactions))
	for _, raw := range block.Transactions {
		tx, err := p.ParseRawTransaction(raw)
		if err != nil {
			return nil, fmt.Errorf("parsing tx %s: %w", raw.Hash, err)
		}

		if bn, ok := parseUint64Hex(block.Number); ok {
			tx.BlockNumber = bn
		}

		txs = append(txs, tx)
	}
	return txs, nil
}

// IsSwapTransaction checks if the transaction input data looks like a DEX swap.
// It examines the 4-byte function selector against known swap signatures.
func IsSwapTransaction(tx *models.Transaction) bool {
	if len(tx.Input) < 4 {
		return false
	}
	selector := hex.EncodeToString(tx.Input[:4])

	// Common DEX swap selectors
	swapSelectors := map[string]bool{
		"38ed1739": true, // swapExactTokensForTokens (Uniswap V2)
		"8803dbee": true, // swapTokensForExactTokens (Uniswap V2)
		"7ff36ab5": true, // swapExactETHForTokens (Uniswap V2)
		"18cbafe5": true, // swapExactTokensForETH (Uniswap V2)
		"c04b8d59": true, // exactInput (Uniswap V3)
		"db3e2198": true, // exactOutputSingle (Uniswap V3)
		"414bf389": true, // exactInputSingle (Uniswap V3)
		"5ae401dc": true, // multicall (Uniswap V3 Router)
	}

	return swapSelectors[selector]
}

// ExtractSwapTokens attempts to extract token addresses from swap calldata.
// Returns token0 (in) and token1 (out) addresses if decodable.
func ExtractSwapTokens(tx *models.Transaction) (tokenIn, tokenOut string, err error) {
	if len(tx.Input) < 68 {
		return "", "", fmt.Errorf("input data too short for swap decoding")
	}

	// For Uniswap V2 style swaps, the path array contains token addresses.
	// This is a simplified extraction; real implementations need full ABI decoding.
	// We look for the path parameter which contains 20-byte addresses.
	selector := hex.EncodeToString(tx.Input[:4])
	switch selector {
	case "38ed1739", "8803dbee":
		// swapExactTokensForTokens / swapTokensForExactTokens
		// path is the 3rd dynamic parameter
		if len(tx.Input) >= 228 {
			tokenIn = "0x" + hex.EncodeToString(tx.Input[196:216])
			// path end is variable, take second address at offset +32
			if len(tx.Input) >= 260 {
				tokenOut = "0x" + hex.EncodeToString(tx.Input[228:248])
			}
		}
	default:
		return "", "", fmt.Errorf("unsupported swap selector: %s", selector)
	}

	return tokenIn, tokenOut, nil
}

// parseBigHex parses a hex string (with 0x prefix) into a *big.Int.
func parseBigHex(s string) (*big.Int, bool) {
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return big.NewInt(0), true
	}
	n := new(big.Int)
	_, ok := n.SetString(s, 16)
	return n, ok
}

// parseUint64Hex parses a hex string into uint64.
func parseUint64Hex(s string) (uint64, bool) {
	n, ok := parseBigHex(s)
	if !ok {
		return 0, false
	}
	return n.Uint64(), true
}

// decodeHexData decodes a 0x-prefixed hex string into bytes.
func decodeHexData(s string) []byte {
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return nil
	}
	data, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return data
}
