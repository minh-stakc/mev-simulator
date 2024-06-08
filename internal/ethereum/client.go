package ethereum

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync/atomic"
	"time"

	"mev_simulator/config"
)

// Client communicates with an Ethereum JSON-RPC node (Geth, Reth, etc.).
type Client struct {
	rpcURL     string
	httpClient *http.Client
	chainID    int64
	maxRetries int
	requestID  atomic.Uint64
}

// NewClient creates a new Ethereum RPC client from configuration.
func NewClient(cfg config.EthereumConfig) *Client {
	return &Client{
		rpcURL: cfg.RPCURL,
		httpClient: &http.Client{
			Timeout: cfg.Timeout(),
		},
		chainID:    cfg.ChainID,
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
	return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message)
}

// call executes a JSON-RPC method with retry logic.
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

// ethBlockResult is the raw JSON structure returned by eth_getBlockByNumber.
type ethBlockResult struct {
	Number       string             `json:"number"`
	Hash         string             `json:"hash"`
	ParentHash   string             `json:"parentHash"`
	Timestamp    string             `json:"timestamp"`
	GasUsed      string             `json:"gasUsed"`
	GasLimit     string             `json:"gasLimit"`
	BaseFeePerGas string            `json:"baseFeePerGas"`
	Miner        string             `json:"miner"`
	Transactions []ethTxResult      `json:"transactions"`
}

// ethTxResult is the raw JSON structure for a transaction within a block.
type ethTxResult struct {
	Hash             string `json:"hash"`
	From             string `json:"from"`
	To               string `json:"to"`
	Value            string `json:"value"`
	GasPrice         string `json:"gasPrice"`
	Gas              string `json:"gas"`
	Nonce            string `json:"nonce"`
	Input            string `json:"input"`
	TransactionIndex string `json:"transactionIndex"`
}

// ethTxReceipt is the raw JSON structure for a transaction receipt.
type ethTxReceipt struct {
	TransactionHash string       `json:"transactionHash"`
	Status          string       `json:"status"`
	GasUsed         string       `json:"gasUsed"`
	Logs            []ethLogResult `json:"logs"`
}

type ethLogResult struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

// GetBlockByNumber fetches a full block with transactions.
func (c *Client) GetBlockByNumber(ctx context.Context, blockNum uint64) (*ethBlockResult, error) {
	hexNum := fmt.Sprintf("0x%x", blockNum)
	result, err := c.call(ctx, "eth_getBlockByNumber", []interface{}{hexNum, true})
	if err != nil {
		return nil, fmt.Errorf("eth_getBlockByNumber(%d): %w", blockNum, err)
	}

	if string(result) == "null" {
		return nil, fmt.Errorf("block %d not found", blockNum)
	}

	var block ethBlockResult
	if err := json.Unmarshal(result, &block); err != nil {
		return nil, fmt.Errorf("parsing block %d: %w", blockNum, err)
	}

	return &block, nil
}

// GetLatestBlockNumber returns the current head block number.
func (c *Client) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	result, err := c.call(ctx, "eth_blockNumber", nil)
	if err != nil {
		return 0, fmt.Errorf("eth_blockNumber: %w", err)
	}

	var hexNum string
	if err := json.Unmarshal(result, &hexNum); err != nil {
		return 0, fmt.Errorf("parsing block number: %w", err)
	}

	num := new(big.Int)
	if _, ok := num.SetString(hexNum, 0); !ok {
		return 0, fmt.Errorf("invalid block number hex: %s", hexNum)
	}

	return num.Uint64(), nil
}

// GetTransactionReceipt fetches the receipt for a given transaction hash.
func (c *Client) GetTransactionReceipt(ctx context.Context, txHash string) (*ethTxReceipt, error) {
	result, err := c.call(ctx, "eth_getTransactionReceipt", []interface{}{txHash})
	if err != nil {
		return nil, fmt.Errorf("eth_getTransactionReceipt(%s): %w", txHash, err)
	}

	if string(result) == "null" {
		return nil, fmt.Errorf("receipt not found for %s", txHash)
	}

	var receipt ethTxReceipt
	if err := json.Unmarshal(result, &receipt); err != nil {
		return nil, fmt.Errorf("parsing receipt for %s: %w", txHash, err)
	}

	return &receipt, nil
}

// TraceTransaction calls debug_traceTransaction for execution traces.
func (c *Client) TraceTransaction(ctx context.Context, txHash string) (json.RawMessage, error) {
	opts := map[string]interface{}{
		"tracer": "callTracer",
	}
	result, err := c.call(ctx, "debug_traceTransaction", []interface{}{txHash, opts})
	if err != nil {
		return nil, fmt.Errorf("debug_traceTransaction(%s): %w", txHash, err)
	}
	return result, nil
}

// ChainID returns the configured chain ID.
func (c *Client) ChainID() int64 {
	return c.chainID
}

// RPCURL returns the configured RPC endpoint.
func (c *Client) RPCURL() string {
	return c.rpcURL
}
