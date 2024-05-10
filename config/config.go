package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the complete application configuration.
type Config struct {
	Ethereum   EthereumConfig   `yaml:"ethereum"`
	Solana     SolanaConfig     `yaml:"solana"`
	Simulation SimulationConfig `yaml:"simulation"`
	Analysis   AnalysisConfig   `yaml:"analysis"`
	Logging    LoggingConfig    `yaml:"logging"`
}

// EthereumConfig holds Ethereum RPC connection settings.
type EthereumConfig struct {
	RPCURL         string `yaml:"rpc_url"`
	ChainID        int64  `yaml:"chain_id"`
	MaxRetries     int    `yaml:"max_retries"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// Timeout returns the configured timeout as a time.Duration.
func (c EthereumConfig) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// SolanaConfig holds Solana RPC connection settings.
type SolanaConfig struct {
	RPCURL         string `yaml:"rpc_url"`
	MaxRetries     int    `yaml:"max_retries"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// Timeout returns the configured timeout as a time.Duration.
func (c SolanaConfig) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// SimulationConfig holds simulation parameters.
type SimulationConfig struct {
	MaxBlocks  int      `yaml:"max_blocks"`
	MaxSlots   int      `yaml:"max_slots"`
	Workers    int      `yaml:"workers"`
	Strategies []string `yaml:"strategies"`
}

// AnalysisConfig holds analysis thresholds and flags.
type AnalysisConfig struct {
	MinProfitWei      string `yaml:"min_profit_wei"`
	MinProfitLamports int64  `yaml:"min_profit_lamports"`
	IncludeFailedTxs  bool   `yaml:"include_failed_txs"`
	TraceDepth        int    `yaml:"trace_depth"`
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Output string `yaml:"output"`
}

// Load reads and parses a YAML configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// validate checks required configuration fields.
func (c *Config) validate() error {
	if c.Ethereum.RPCURL == "" {
		return fmt.Errorf("ethereum.rpc_url is required")
	}
	if c.Solana.RPCURL == "" {
		return fmt.Errorf("solana.rpc_url is required")
	}
	if c.Simulation.MaxBlocks <= 0 {
		c.Simulation.MaxBlocks = 100
	}
	if c.Simulation.MaxSlots <= 0 {
		c.Simulation.MaxSlots = 100
	}
	if c.Simulation.Workers <= 0 {
		c.Simulation.Workers = 4
	}
	if len(c.Simulation.Strategies) == 0 {
		c.Simulation.Strategies = []string{"arbitrage", "sandwich", "liquidation"}
	}
	return nil
}
