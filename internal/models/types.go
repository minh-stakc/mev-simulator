package models

import (
	"math/big"
	"time"
)

// Chain identifies which blockchain a piece of data belongs to.
type Chain string

const (
	ChainEthereum Chain = "ethereum"
	ChainSolana   Chain = "solana"
)

// StrategyType identifies the MEV strategy being evaluated.
type StrategyType string

const (
	StrategyArbitrage   StrategyType = "arbitrage"
	StrategySandwich    StrategyType = "sandwich"
	StrategyLiquidation StrategyType = "liquidation"
)

// Transaction is a unified representation of a transaction across chains.
type Transaction struct {
	Hash        string
	Chain       Chain
	BlockNumber uint64
	SlotNumber  uint64 // Solana only
	From        string
	To          string
	Value       *big.Int
	GasPrice    *big.Int // Ethereum: gas price; Solana: fee in lamports
	GasUsed     uint64
	Nonce       uint64
	Input       []byte
	Status      TxStatus
	Timestamp   time.Time
	Index       int // position within block/slot
	Logs        []LogEntry
}

// TxStatus represents the execution status of a transaction.
type TxStatus int

const (
	TxStatusUnknown TxStatus = iota
	TxStatusSuccess
	TxStatusFailed
	TxStatusPending
)

// String returns a human-readable status label.
func (s TxStatus) String() string {
	switch s {
	case TxStatusSuccess:
		return "success"
	case TxStatusFailed:
		return "failed"
	case TxStatusPending:
		return "pending"
	default:
		return "unknown"
	}
}

// LogEntry represents a decoded event log from a transaction.
type LogEntry struct {
	Address string
	Topics  []string
	Data    []byte
}

// Block is a unified representation of a block (Ethereum) or slot (Solana).
type Block struct {
	Chain        Chain
	Number       uint64
	Hash         string
	ParentHash   string
	Timestamp    time.Time
	Transactions []Transaction
	GasUsed      uint64
	GasLimit     uint64
	BaseFee      *big.Int // Ethereum EIP-1559 base fee
	Miner        string   // Ethereum: miner/validator; Solana: leader
}

// TransactionTrace represents an internal call trace for a transaction.
type TransactionTrace struct {
	TxHash   string
	Chain    Chain
	Calls    []TraceCall
	GasUsed  uint64
	Depth    int
	Reverted bool
}

// TraceCall represents a single internal call within a trace.
type TraceCall struct {
	Type    string // CALL, DELEGATECALL, STATICCALL, CREATE, etc.
	From    string
	To      string
	Value   *big.Int
	GasUsed uint64
	Input   []byte
	Output  []byte
	Depth   int
	Error   string
}

// MEVOpportunity represents a detected MEV extraction opportunity.
type MEVOpportunity struct {
	ID           string
	Strategy     StrategyType
	Chain        Chain
	Transactions []Transaction
	Revenue      *big.Int
	Cost         *big.Int
	Profit       *big.Int
	BlockNumber  uint64
	Timestamp    time.Time
	Details      map[string]string
}

// NetProfit computes revenue minus cost, returning a new big.Int.
func (o *MEVOpportunity) NetProfit() *big.Int {
	if o.Revenue == nil || o.Cost == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Sub(o.Revenue, o.Cost)
}

// IsProfitable reports whether the opportunity yields positive net profit.
func (o *MEVOpportunity) IsProfitable() bool {
	return o.NetProfit().Sign() > 0
}

// CrossChainOpportunity links MEV opportunities across two chains.
type CrossChainOpportunity struct {
	EthereumOpp   *MEVOpportunity
	SolanaOpp     *MEVOpportunity
	CombinedProfit *big.Int
	BridgeCost     *big.Int
	TimeDelta      time.Duration
	Feasible       bool
}

// SimulationResult holds the output of a complete simulation run.
type SimulationResult struct {
	StartBlock         uint64
	EndBlock           uint64
	StartSlot          uint64
	EndSlot            uint64
	TotalTransactions  int
	Opportunities      []MEVOpportunity
	CrossChainOpps     []CrossChainOpportunity
	TotalRevenue       *big.Int
	TotalCost          *big.Int
	TotalProfit        *big.Int
	Duration           time.Duration
	StrategyBreakdown  map[StrategyType]StrategyStats
}

// StrategyStats holds aggregate statistics for a single strategy type.
type StrategyStats struct {
	Strategy       StrategyType
	Count          int
	TotalRevenue   *big.Int
	TotalCost      *big.Int
	TotalProfit    *big.Int
	SuccessRate    float64
	AvgProfitPerOp *big.Int
}

// PriceQuote represents a token price at a point in time on a DEX.
type PriceQuote struct {
	Token0   string
	Token1   string
	Price    *big.Float
	Pool     string
	Chain    Chain
	BlockNum uint64
}

// LiquidationTarget represents a borrowing position that may be liquidatable.
type LiquidationTarget struct {
	Protocol       string
	Borrower       string
	CollateralToken string
	DebtToken       string
	CollateralValue *big.Int
	DebtValue       *big.Int
	HealthFactor    *big.Float
	Chain           Chain
	BlockNumber     uint64
}
