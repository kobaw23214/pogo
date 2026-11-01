package sentinelexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/sentinel"
)

const diagnosticCanary = "SYNTHETIC_SOURCE_CREDENTIAL_7bd32a9f"

func TestSentinelDiagnosticFixture(t *testing.T) {
	if os.Getenv("GO_SENTINEL_EXEC_FIXTURE") != "1" {
		return
	}
	var boot any
	if json.NewDecoder(os.Stdin).Decode(&boot) != nil {
		os.Exit(8)
	}
	switch os.Args[len(os.Args)-1] {
	case "stderr-secret":
		fmt.Fprintln(os.Stderr, "Authorization: Bearer "+diagnosticCanary)
		os.Exit(7)
	case "unknown-field-secret":
		fmt.Fprintf(os.Stdout, "{\"v\":1,\"id\":\"x\",\"events\":[],%q:true}\n", diagnosticCanary)
	case "success-stderr":
		fmt.Fprintln(os.Stderr, "password="+diagnosticCanary)
		fmt.Fprintln(os.Stdout, `{"v":1,"id":"healthy","events":[],"cursor":{"position":1}}`)
	case "declared-capacity", "declared-capacity-after-frame":
		if os.Args[len(os.Args)-1] == "declared-capacity-after-frame" {
			fmt.Fprintln(os.Stdout, `{"v":1,"id":"partial","events":[],"state":{"partial":true},"cursor":{"position":999}}`)
		}
		fmt.Fprintln(os.Stderr, "password="+diagnosticCanary)
		os.Exit(80)
	case "declared-incomplete":
		os.Exit(81)
	case "declared-unavailable":
		os.Exit(82)
	case "declared-history":
		os.Exit(83)
	case "declared-after-wait":
		fmt.Fprintln(os.Stderr, "DECLARED_EXIT_READY")
		time.Sleep(time.Second)
		os.Exit(80)
	}
	os.Exit(0)
}

func TestDeclaredObservationFailuresAreVisibleAndCannotCommitPollProgress(t *testing.T) {
	for mode, reason := range map[string]string{
		"declared-capacity":             "capacity_exceeded",
		"declared-capacity-after-frame": "capacity_exceeded",
		"declared-incomplete":           "incomplete_observation",
		"declared-unavailable":          "source_unavailable",
		"declared-history":              "history_invalid",
	} {
		t.Run(mode, func(t *testing.T) {
			r, cfg := fixture(t, mode)
			cfg.Command[1] = "-test.run=^TestSentinelDiagnosticFixture$"
			cfg.State, cfg.Cursor = json.RawMessage(`{"complete":7}`), json.RawMessage(`{"position":7}`)
			accepted := 0
			err := r.Run(context.Background(), cfg, func(sentinel.Frame) error { accepted++; return nil })
			if err == nil || !strings.Contains(err.Error(), reason+":") || strings.Contains(err.Error(), diagnosticCanary) || accepted != 0 {
				t.Fatalf("declared failure lost its safe diagnostic or committed partial work: err=%v accepted=%d", err, accepted)
			}
			if string(cfg.State) != `{"complete":7}` || string(cfg.Cursor) != `{"position":7}` {
				t.Fatal("a declared failure reset prior invocation state/cursor")
			}
		})
	}
}

func TestCancellationAndDeadlineAreNotReportedAsObservationCapacity(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%v", deadline), func(t *testing.T) {
			r, cfg := fixture(t, "declared-after-wait")
			cfg.Command[1] = "-test.run=^TestSentinelDiagnosticFixture$"
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
				want = context.DeadlineExceeded
			} else {
				r.OnLog = func(text string) {
					if strings.Contains(text, "DECLARED_EXIT_READY") {
						cancel()
					}
				}
			}
			defer cancel()
			accepted := 0
			err := r.Run(ctx, cfg, func(sentinel.Frame) error { accepted++; return nil })
			if !errors.Is(err, want) || strings.Contains(err.Error(), "capacity_exceeded") || accepted != 0 {
				t.Fatalf("cancellation mislabeled: err=%v want=%v accepted=%d", err, want, accepted)
			}
		})
	}
}

func TestPublicRunnerDiagnosticsDoNotEchoScriptSecrets(t *testing.T) {
	for _, mode := range []string{"stderr-secret", "unknown-field-secret", "success-stderr"} {
		t.Run(mode, func(t *testing.T) {
			r, cfg := fixture(t, mode)
			cfg.Command[1] = "-test.run=^TestSentinelDiagnosticFixture$"
			accepted := 0
			err := r.Run(context.Background(), cfg, func(f sentinel.Frame) error {
				accepted++
				if f.ID != "healthy" || string(f.Cursor) != `{"position":1}` {
					t.Error("diagnostic handling changed a valid frame")
				}
				return nil
			})
			if mode == "success-stderr" {
				if err != nil || accepted != 1 {
					t.Fatalf("successful script diagnostics changed acceptance: err=%v accepted=%d", err, accepted)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), diagnosticCanary) || accepted != 0 {
				t.Fatalf("public diagnostic leaked source bytes or accepted invalid output: err=%v accepted=%d", err, accepted)
			}
			if mode == "stderr-secret" && (!strings.Contains(err.Error(), "exit status 7") || !strings.Contains(err.Error(), "stderr withheld:")) {
				t.Fatalf("safe exit diagnostic lost actionable metadata: %v", err)
			}
			if mode == "unknown-field-secret" && !strings.Contains(err.Error(), "invalid sentinel frame") {
				t.Fatalf("malformed frame diagnostic lost its category: %v", err)
			}
		})
	}
}

func TestMalformedFrameDiagnosticsNeverQuoteUnknownFieldPaths(t *testing.T) {
	for _, raw := range []string{
		`{"v":1,"id":"x","events":[],"` + diagnosticCanary + `":true}`,
		`{"v":1,"id":"x","events":[{"key":"x","summary":"x","` + diagnosticCanary + `":true}]}`,
		`{"v":1,"id":"x","events":{ "` + diagnosticCanary + `": true }}`,
	} {
		_, err := decodeFrame([]byte(raw))
		if err == nil || strings.Contains(err.Error(), diagnosticCanary) || len(err.Error()) > 256 {
			t.Fatalf("raw field name escaped via decoder: %v", err)
		}
	}
}
