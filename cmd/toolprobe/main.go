// toolprobe exercises a configured model using only a local nonce fixture.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

func main() {
	model := flag.String("model", "", "model ID or alias from providers.json")
	timeout := flag.Duration("timeout", 2*time.Minute, "deadline for the entire probe")
	suite := flag.String("suite", "basic", "basic (nonce) or contract (nonce and typed arguments)")
	repeat := flag.Int("repeat", 1, "independent runs per case, from 1 to 20")
	flag.Parse()
	if (*suite != "basic" && *suite != "contract") || *repeat < 1 || *repeat > 20 || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "suite must be basic or contract; repeat must be 1..20; timeout must be positive")
		os.Exit(2)
	}
	if _, err := config.Load(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg, err := config.Resolve(*model)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := llm.New(llm.Options{BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model,
		Headers: cfg.Headers, ExtraBody: cfg.ExtraBody, MaxTokens: cfg.MaxTokens,
		Compatibility: cfg.Compatibility, MaxRetries: -1})
	// Preserve the original report shape for existing basic-probe consumers.
	var report any
	passed := false
	if *suite == "basic" && *repeat == 1 {
		result := llm.ProbeToolCalls(ctx, client)
		report, passed = result, result.Passed
	} else {
		result := runSuite(ctx, client, *suite, *repeat, cfg.Compatibility)
		report, passed = result, result.Passed
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		os.Exit(2)
	}
	if !passed {
		os.Exit(1)
	}
}

type suiteCase struct {
	Name string `json:"name"`
	Run  int    `json:"run"`
	llm.ToolProbeReport
}

type suiteReport struct {
	Model         string            `json:"model"`
	Compatibility llm.Compatibility `json:"compatibility"`
	Suite         string            `json:"suite"`
	Passed        bool              `json:"passed"`
	Requested     int               `json:"requested"`
	Completed     int               `json:"completed"`
	FirstPass     int               `json:"first_pass"`
	Recovered     int               `json:"recovered"`
	Cases         []suiteCase       `json:"cases"`
}

func runSuite(ctx context.Context, client llm.Client, suite string, repeat int, compat llm.Compatibility) suiteReport {
	report := suiteReport{Model: client.Model(), Compatibility: compat, Suite: suite, Passed: true}
	tests := []struct {
		name  string
		probe func(context.Context, llm.Client) llm.ToolProbeReport
	}{
		{"nonce", llm.ProbeToolCalls},
	}
	if suite == "contract" {
		tests = append(tests, struct {
			name  string
			probe func(context.Context, llm.Client) llm.ToolProbeReport
		}{"arguments", llm.ProbeToolArguments})
	}
	report.Requested = repeat * len(tests)
	for run := 1; run <= repeat; run++ {
		for _, test := range tests {
			if ctx.Err() != nil {
				report.Passed = false
				return report
			}
			result := test.probe(ctx, client)
			report.Cases = append(report.Cases, suiteCase{Name: test.name, Run: run, ToolProbeReport: result})
			report.Completed++
			report.Passed = report.Passed && result.Passed
			if result.Passed && result.Corrections == 0 {
				report.FirstPass++
			} else if result.Passed {
				report.Recovered++
			}
		}
	}
	return report
}
