package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"mev_simulator/config"
	"mev_simulator/internal/analysis"
	"mev_simulator/internal/ethereum"
	"mev_simulator/internal/models"
	"mev_simulator/internal/simulator"
	"mev_simulator/internal/solana"
	"mev_simulator/internal/strategies"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to configuration file")
	ethStart := flag.Uint64("eth-start", 0, "Ethereum start block number (0 = latest - max_blocks)")
	ethEnd := flag.Uint64("eth-end", 0, "Ethereum end block number (0 = latest)")
	solStart := flag.Uint64("sol-start", 0, "Solana start slot (0 = latest - max_slots)")
	solEnd := flag.Uint64("sol-end", 0, "Solana end slot (0 = latest)")
	mode := flag.String("mode", "both", "simulation mode: ethereum, solana, both, crosschain")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("MEV Simulator starting")
	log.Printf("  Ethereum RPC: %s (chain %d)", cfg.Ethereum.RPCURL, cfg.Ethereum.ChainID)
	log.Printf("  Solana RPC:   %s", cfg.Solana.RPCURL)
	log.Printf("  Strategies:   %s", strings.Join(cfg.Simulation.Strategies, ", "))
	log.Printf("  Workers:      %d", cfg.Simulation.Workers)
	log.Printf("  Mode:         %s", *mode)

	// Set up context with graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()

	// Initialize chain clients.
	ethClient := ethereum.NewClient(cfg.Ethereum)
	solClient := solana.NewClient(cfg.Solana)

	// Initialize block fetchers.
	ethAnalyzer := ethereum.NewBlockAnalyzer(ethClient)
	solAnalyzer := solana.NewSlotAnalyzer(solClient)

	ethFetcher := &simulator.EthereumBlockFetcher{Fetcher: ethAnalyzer}
	solFetcher := &simulator.SolanaBlockFetcher{Fetcher: solAnalyzer}

	// Create the simulation engine.
	engine := simulator.NewEngine(cfg.Simulation, ethFetcher, solFetcher)

	// Parse minimum profit threshold.
	minProfit := big.NewInt(1e15) // default 0.001 ETH
	if cfg.Analysis.MinProfitWei != "" {
		if p, ok := new(big.Int).SetString(cfg.Analysis.MinProfitWei, 10); ok {
			minProfit = p
		}
	}

	// Register configured strategies.
	strategySet := make(map[string]bool)
	for _, s := range cfg.Simulation.Strategies {
		strategySet[s] = true
	}

	if strategySet["arbitrage"] {
		engine.RegisterStrategy(strategies.NewArbitrageDetector(minProfit))
		log.Printf("  Registered strategy: arbitrage")
	}
	if strategySet["sandwich"] {
		engine.RegisterStrategy(strategies.NewSandwichDetector(minProfit, 5.0))
		log.Printf("  Registered strategy: sandwich")
	}
	if strategySet["liquidation"] {
		engine.RegisterStrategy(strategies.NewLiquidationDetector(minProfit))
		log.Printf("  Registered strategy: liquidation")
	}

	// Resolve block/slot ranges.
	ethStartBlock, ethEndBlock := *ethStart, *ethEnd
	solStartSlot, solEndSlot := *solStart, *solEnd

	if needsEthereum(*mode) {
		if ethEndBlock == 0 {
			latest, err := ethClient.GetLatestBlockNumber(ctx)
			if err != nil {
				log.Fatalf("Failed to get latest Ethereum block: %v", err)
			}
			ethEndBlock = latest
			log.Printf("  Latest Ethereum block: %d", latest)
		}
		if ethStartBlock == 0 {
			ethStartBlock = ethEndBlock - uint64(cfg.Simulation.MaxBlocks)
		}
		log.Printf("  Ethereum range: %d - %d (%d blocks)", ethStartBlock, ethEndBlock, ethEndBlock-ethStartBlock+1)
	}

	if needsSolana(*mode) {
		if solEndSlot == 0 {
			latest, err := solClient.GetLatestSlot(ctx)
			if err != nil {
				log.Fatalf("Failed to get latest Solana slot: %v", err)
			}
			solEndSlot = latest
			log.Printf("  Latest Solana slot: %d", latest)
		}
		if solStartSlot == 0 {
			solStartSlot = solEndSlot - uint64(cfg.Simulation.MaxSlots)
		}
		log.Printf("  Solana range: %d - %d (%d slots)", solStartSlot, solEndSlot, solEndSlot-solStartSlot+1)
	}

	// Run simulation.
	var result *models.SimulationResult

	switch *mode {
	case "ethereum":
		log.Printf("Running Ethereum-only simulation...")
		result, err = engine.RunEthereum(ctx, ethStartBlock, ethEndBlock)

	case "solana":
		log.Printf("Running Solana-only simulation...")
		result, err = engine.RunSolana(ctx, solStartSlot, solEndSlot)

	case "crosschain":
		log.Printf("Running cross-chain simulation...")
		crossAnalyzer := simulator.NewCrossChainAnalyzer(engine)
		result, err = crossAnalyzer.AnalyzeWindow(ctx, ethStartBlock, ethEndBlock, solStartSlot, solEndSlot)

	default: // "both"
		log.Printf("Running dual-chain simulation...")
		ethResult, ethErr := engine.RunEthereum(ctx, ethStartBlock, ethEndBlock)
		if ethErr != nil {
			log.Printf("Ethereum simulation error: %v", ethErr)
		}

		solResult, solErr := engine.RunSolana(ctx, solStartSlot, solEndSlot)
		if solErr != nil {
			log.Printf("Solana simulation error: %v", solErr)
		}

		result = mergeResults(ethResult, solResult)
		if ethErr != nil && solErr != nil {
			err = fmt.Errorf("both chains failed: eth=%v, sol=%v", ethErr, solErr)
		}
	}

	if err != nil {
		log.Fatalf("Simulation failed: %v", err)
	}

	// Generate profitability report.
	profitAnalyzer := analysis.NewProfitabilityAnalyzer(3500.0, 150.0)
	report := profitAnalyzer.Analyze(result)

	fmt.Println()
	fmt.Println(analysis.FormatReport(report))

	// Print trace analysis summary.
	traceAnalyzer := analysis.NewTraceAnalyzer(cfg.Analysis.TraceDepth)
	_ = traceAnalyzer // trace analysis requires individual tx hashes; shown for completeness

	log.Printf("Simulation complete. Duration: %v", result.Duration)
	log.Printf("Total transactions analyzed: %d", result.TotalTransactions)
	log.Printf("Total opportunities found: %d", len(result.Opportunities))
	log.Printf("Cross-chain opportunities: %d", len(result.CrossChainOpps))
}

// mergeResults combines two simulation results into one.
func mergeResults(a, b *models.SimulationResult) *models.SimulationResult {
	if a == nil && b == nil {
		return &models.SimulationResult{
			TotalRevenue:      big.NewInt(0),
			TotalCost:         big.NewInt(0),
			TotalProfit:       big.NewInt(0),
			StrategyBreakdown: make(map[models.StrategyType]models.StrategyStats),
		}
	}
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}

	merged := &models.SimulationResult{
		StartBlock:        a.StartBlock,
		EndBlock:          a.EndBlock,
		StartSlot:         b.StartSlot,
		EndSlot:           b.EndSlot,
		TotalTransactions: a.TotalTransactions + b.TotalTransactions,
		Opportunities:     append(a.Opportunities, b.Opportunities...),
		CrossChainOpps:    append(a.CrossChainOpps, b.CrossChainOpps...),
		TotalRevenue:      new(big.Int).Add(safeInt(a.TotalRevenue), safeInt(b.TotalRevenue)),
		TotalCost:         new(big.Int).Add(safeInt(a.TotalCost), safeInt(b.TotalCost)),
		TotalProfit:       new(big.Int).Add(safeInt(a.TotalProfit), safeInt(b.TotalProfit)),
		Duration:          a.Duration + b.Duration,
		StrategyBreakdown: make(map[models.StrategyType]models.StrategyStats),
	}

	// Merge strategy breakdowns.
	for st, stats := range a.StrategyBreakdown {
		merged.StrategyBreakdown[st] = stats
	}
	for st, stats := range b.StrategyBreakdown {
		existing, ok := merged.StrategyBreakdown[st]
		if !ok {
			merged.StrategyBreakdown[st] = stats
			continue
		}
		existing.Count += stats.Count
		if existing.TotalRevenue != nil && stats.TotalRevenue != nil {
			existing.TotalRevenue.Add(existing.TotalRevenue, stats.TotalRevenue)
		}
		if existing.TotalCost != nil && stats.TotalCost != nil {
			existing.TotalCost.Add(existing.TotalCost, stats.TotalCost)
		}
		if existing.TotalProfit != nil && stats.TotalProfit != nil {
			existing.TotalProfit.Add(existing.TotalProfit, stats.TotalProfit)
		}
		merged.StrategyBreakdown[st] = existing
	}

	return merged
}

func safeInt(n *big.Int) *big.Int {
	if n == nil {
		return big.NewInt(0)
	}
	return n
}

func needsEthereum(mode string) bool {
	return mode == "ethereum" || mode == "both" || mode == "crosschain"
}

func needsSolana(mode string) bool {
	return mode == "solana" || mode == "both" || mode == "crosschain"
}
