package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/redact"
	"github.com/shakerShekhYousef/flowguard/internal/report"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
	"github.com/shakerShekhYousef/flowguard/internal/scenarios"
)

const defaultTimeout = 10 * time.Second

const version = "0.1.0-dev"

// Exit codes per FR-07: 0 = pass, 1 = assertion failure, 2 = config/runtime error.
const (
	exitOK            = 0
	exitAssertionFail = 1
	exitRuntimeError  = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return exitRuntimeError
	}

	switch args[0] {
	case "version", "-v", "--version":
		fmt.Println("flowguard version " + version)
		return exitOK
	case "help", "-h", "--help":
		printUsage()
		return exitOK
	case "test":
		return runTest(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		printUsage()
		return exitRuntimeError
	}
}

func runTest(args []string) int {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "path to flowguard.yaml")
	reportPath := fs.String("report", "", "path to write the JSON report")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitRuntimeError
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "error: --config <path> is required")
		return exitRuntimeError
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", redact.Error(err))
		return exitRuntimeError
	}

	client := runner.New(cfg.Target.AllowedHosts, defaultTimeout)
	ctx := context.Background()

	fmt.Println("FlowGuard v" + version)

	var results []scenarios.Result
	runtimeErr := false
	assertionFail := false

	for _, sc := range cfg.Scenarios {
		fmt.Printf("\nRunning: %s\n", sc.Name)

		var result scenarios.Result
		switch sc.Kind {
		case "idempotency":
			result = scenarios.RunIdempotency(ctx, client, cfg.Target.BaseURL, sc)
		default:
			fmt.Fprintf(os.Stderr, "error: scenario %q has unsupported kind %q\n", sc.Name, sc.Kind)
			return exitRuntimeError
		}
		results = append(results, result)

		if result.Err != nil {
			fmt.Printf("  [ERROR] %s\n", redact.Error(result.Err))
			runtimeErr = true
			continue
		}
		for _, a := range result.Assertions {
			if a.Passed {
				fmt.Printf("  [PASS] %s\n", a.Rule)
			} else {
				fmt.Printf("  [FAIL] %s\n", redact.Text(a.Reason))
				assertionFail = true
			}
		}
	}

	if *reportPath != "" {
		if err := report.Write(*reportPath, report.Build(results)); err != nil {
			fmt.Fprintf(os.Stderr, "error: writing report: %s\n", redact.Error(err))
			return exitRuntimeError
		}
		fmt.Printf("\nreport written to %s\n", *reportPath)
	}

	switch {
	case runtimeErr:
		return exitRuntimeError
	case assertionFail:
		return exitAssertionFail
	default:
		return exitOK
	}
}

func printUsage() {
	fmt.Println(`FlowGuard — reproducible business-invariant failure testing for APIs

Usage:
  flowguard <command> [flags]

Commands:
  test --config <file> [--report <file>]   Run scenarios from a flowguard.yaml file
  version                                  Print the version
  help                                     Show this message`)
}
