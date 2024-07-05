package solana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"mev_simulator/config"
)

// Client communicates with a Solana JSON-RPC node.
type Client struct {
	rpcURL     string
	httpClient *http.Client
	maxRetries int
	requestID  atomic.Uint64
}

// NewClient creates a new Solana RPC client from configuration.
func NewClient(cfg config.SolanaConfig) *Client {
	return &Client{
		rpcURL: cfg.RPCURL,
		httpClient: &http.Client{
			Timeout: cfg.Timeout(),
		},
		maxRetries: cfg.MaxRetries,
	}
}

// jsonRPCRequest is the standard JSON-RPC 2.0 request envelope.
type jsonRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      uint64        `json:"id"`
}

// jsonRPCResponse is the standard JSON-RPC 2.0 response envelope.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("Solana RPC error %d: %s", e.Code, e.Message)
}

// call executes a Solana JSON-RPC method with retry logic.
func (c *Client) call(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	id := c.requestID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	var lastErr error
	attempts := c.maxRetries
	if attempts <= 0 {
		attempts = 1
	}

	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(i) * 500 * time.Millisecond):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("creating HTTP request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("HTTP request failed: %w", err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("reading response body: %w", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(respBody))
			continue
		}

		var rpcResp jsonRPCResponse
		if err := json.Unmarshal(respBody, &rpcResp); err != nil {
			lastErr = fmt.Errorf("unmarshaling response: %w", err)
			continue
		}

		if rpcResp.Error != nil {
			return nil, rpcResp.Error
		}

		return rpcResp.Result, nil
	}

	return nil, fmt.Errorf("all %d attempts failed, last error: %w", attempts, lastErr)
}

// SlotResult holds the parsed result of getBlock.
type SlotResult struct {
	BlockHeight       *uint64                `json:"blockHeight"`
	BlockTime         *int64                 `json:"blockTime"`
	Blockhash         string                 `json:"blockhash"`
	ParentSlot        uint64                 `json:"parentSlot"`
	PreviousBlockhash string                 `json:"previousBlockhash"`
	Transactions      []SlotTransactionResult `json:"transactions"`
}

// SlotTransactionResult is a transaction entry within a Solana block.
type SlotTransactionResult struct {
	Transaction json.RawMessage    `json:"transaction"`
	Meta        *TransactionMeta   `json:"meta"`
}

// TransactionMeta holds the metadata of a Solana transaction.
type TransactionMeta struct {
	Err               interface{} `json:"err"`
	Fee               uint64      `json:"fee"`
	PreBalances       []uint64    `json:"preBalances"`
	PostBalances      []uint64    `json:"postBalances"`
	PreTokenBalances  []TokenBalance `json:"preTokenBalances"`
	PostTokenBalances []TokenBalance `json:"postTokenBalances"`
	LogMessages       []string    `json:"logMessages"`
	InnerInstructions []InnerInstruction `json:"innerInstructions"`
}

// TokenBalance represents a token account balance snapshot.
type TokenBalance struct {
	AccountIndex  int    `json:"accountIndex"`
	Mint          string `json:"mint"`
	Owner         string `json:"owner"`
	UITokenAmount struct {
		Amount         string  `json:"amount"`
		Decimals       int     `json:"decimals"`
		UIAmountString string  `json:"uiAmountString"`
		UIAmount       float64 `json:"uiAmount"`
	} `json:"uiTokenAmount"`
}

// InnerInstruction represents a CPI (cross-program invocation) result.
type InnerInstruction struct {
	Index        int                 `json:"index"`
	Instructions []InstructionEntry  `json:"instructions"`
}

// InstructionEntry is a single instruction within a transaction.
type InstructionEntry struct {
	ProgramIDIndex int    `json:"programIdIndex"`
	Accounts       []int  `json:"accounts"`
	Data           string `json:"data"`
}

// GetSlot retrieves a full block (slot) with transaction details.
func (c *Client) GetSlot(ctx context.Context, slot uint64) (*SlotResult, error) {
	opts := map[string]interface{}{
		"encoding":                       "json",
		"transactionDetails":             "full",
		"maxSupportedTransactionVersion": 0,
	}
	result, err := c.call(ctx, "getBlock", []interface{}{slot, opts})
	if err != nil {
		return nil, fmt.Errorf("getBlock(%d): %w", slot, err)
	}

	if string(result) == "null" {
		return nil, fmt.Errorf("slot %d not found or skipped", slot)
	}

	var slotResult SlotResult
	if err := json.Unmarshal(result, &slotResult); err != nil {
		return nil, fmt.Errorf("parsing slot %d: %w", slot, err)
	}

	return &slotResult, nil
}

// GetLatestSlot returns the most recently confirmed slot number.
func (c *Client) GetLatestSlot(ctx context.Context) (uint64, error) {
	result, err := c.call(ctx, "getSlot", []interface{}{
		map[string]string{"commitment": "confirmed"},
	})
	if err != nil {
		return 0, fmt.Errorf("getSlot: %w", err)
	}

	var slot uint64
	if err := json.Unmarshal(result, &slot); err != nil {
		return 0, fmt.Errorf("parsing slot number: %w", err)
	}

	return slot, nil
}

// LeaderScheduleEntry maps a validator pubkey to its assigned slots.
type LeaderScheduleEntry map[string][]uint64

// GetLeaderSchedule fetches the leader schedule for the epoch containing the given slot.
func (c *Client) GetLeaderSchedule(ctx context.Context, slot uint64) (LeaderScheduleEntry, error) {
	result, err := c.call(ctx, "getLeaderSchedule", []interface{}{slot})
	if err != nil {
		return nil, fmt.Errorf("getLeaderSchedule(%d): %w", slot, err)
	}

	if string(result) == "null" {
		return nil, fmt.Errorf("no leader schedule for slot %d", slot)
	}

	var schedule LeaderScheduleEntry
	if err := json.Unmarshal(result, &schedule); err != nil {
		return nil, fmt.Errorf("parsing leader schedule: %w", err)
	}

	return schedule, nil
}

// GetTransaction fetches a single transaction by signature.
func (c *Client) GetTransaction(ctx context.Context, signature string) (*SlotTransactionResult, error) {
	opts := map[string]interface{}{
		"encoding":                       "json",
		"maxSupportedTransactionVersion": 0,
	}
	result, err := c.call(ctx, "getTransaction", []interface{}{signature, opts})
	if err != nil {
		return nil, fmt.Errorf("getTransaction(%s): %w", signature, err)
	}

	if string(result) == "null" {
		return nil, fmt.Errorf("transaction %s not found", signature)
	}

	var txResult SlotTransactionResult
	if err := json.Unmarshal(result, &txResult); err != nil {
		return nil, fmt.Errorf("parsing transaction %s: %w", signature, err)
	}

	return &txResult, nil
}

// RPCURL returns the configured RPC endpoint.
func (c *Client) RPCURL() string {
	return c.rpcURL
}
