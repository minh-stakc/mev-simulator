# Cross-chain MEV Simulator

A Go-based simulation environment for analyzing Maximal Extractable Value (MEV) strategies across Ethereum and Solana blockchains.

## Overview

This tool connects to Ethereum and Solana RPC nodes to replay historical transactions and evaluate MEV extraction opportunities including arbitrage, sandwich attacks, and liquidations. It processes transaction traces to study block inclusion patterns and validator ordering behavior.

## Features

- **Cross-chain simulation**: Integrates Ethereum (Geth/Reth) and Solana RPC endpoints to fetch and replay historical transaction data.
- **MEV strategy evaluation**: Implements arbitrage detection, sandwich attack simulation, and liquidation opportunity analysis on replayed transaction sets.
- **Transaction trace analysis**: Processes execution traces to study gas pricing, priority fee dynamics, block inclusion ordering, and validator/leader behavior.
- **Profitability reporting**: Computes ROI, net profit, gas/fee costs, and cross-chain opportunity metrics.

## Project Structure

```
mev_simulator/
  cmd/simulator/main.go          Entry point
  internal/
    ethereum/                     Ethereum RPC client, tx parsing, block analysis
    solana/                       Solana RPC client, tx parsing, slot analysis
    simulator/                    Replay engine and cross-chain coordinator
    strategies/                   Arbitrage, sandwich, liquidation strategies
    analysis/                     Profitability and trace analysis
    models/                       Shared data types
  config/                         Configuration loading and sample YAML
```

## Configuration

Copy and edit `config/config.yaml`:

```yaml
ethereum:
  rpc_url: "http://localhost:8545"
  chain_id: 1
solana:
  rpc_url: "http://localhost:8899"
simulation:
  max_blocks: 100
  strategies:
    - arbitrage
    - sandwich
    - liquidation
```

## Usage

```bash
go build -o mev_simulator ./cmd/simulator
./mev_simulator -config config/config.yaml
```

## Requirements

- Go 1.22+
- Access to Ethereum RPC node (Geth, Reth, or archive node)
- Access to Solana RPC node

## License

MIT
