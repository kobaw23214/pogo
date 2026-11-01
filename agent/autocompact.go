package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// CompactConfig is the auto-compaction policy: the prompt size at which the
// driver should run Agent.Compact between runs. A zero Threshold disables it,
// which is the default — the manual /compact path never consults this.
//
// The agent itself never compacts on this config. v1's trigger is the driver's
// (main.go's run loop, web's Session.Start), between runs, because compaction
// has to move the session file and the persistence counters along with the
// history and those live above the loop; see docs/auto-compact.md for why that
// is the whole of v1 and what the in-run version costs.
type CompactConfig struct {
	// Threshold is the estimated prompt size that arms compaction, in the same
	// unit promptTokens reports — part the provider's own count, part estimate.
	Threshold int64
}

// CompactReserveFloor is the smallest headroom "-compact auto" keeps between
// the compaction threshold and the model's window.
//
// It has to cover the same three things the clearing margin does (see
// ParseContextEdit): the reply the model is about to write, the error in an
// estimate that is only part measured, and the growth of the run that starts
// after the check. 16384 is the value pi ships for the same role, and pi's
// check runs at the same place this one does — before the next user prompt.
const CompactReserveFloor = 16_384

// CompactKeepRecent is the verbatim tail the automatic paths keep through a
// compaction (CompactKeep). 20000 is the value pi and omp ship as their
// keepRecentTokens default and Codex budgets for retained user messages
// (COMPACT_USER_MESSAGE_MAX_TOKENS); Claude Code's window lands at 10–40K,
// Hermes protects the last 20 messages. The WorkOS tuning guide recommends
// 40000 for tool-heavy work on the argument that the tail is "the model's only
// verbatim transcript memory after compaction" — a knob, not a law, and one
// this constant deliberately does not expose until pogo has its own sessions
// to measure.
const CompactKeepRecent int64 = 20_000

// ParseCompactSpec resolves the -compact flag against the model's window.
//
// "" and "off" are the default: compaction stays manual. "auto" keeps a reserve
// of max(CompactReserveFloor, a twentieth of the window), which on a 200K
// window arms at ~92% and on a 1M window at ~98% — later than the SOTA crowd's
// 80-84%, for a reason specific to this harness: clearing already runs
// automatically at four fifths, so by the time compaction is even reachable the
// prompt is mostly conversation rather than tool output, and the thing a lower
// threshold would buy — reclaiming tool output — is clearing's job, done
// reversibly. "auto:N%" and "auto:N" (a token count) override for the people
// who read the reports arguing for earlier compaction (context rot degrades
// quality well before the window fills).
//
// Like ParseContextEdit, a model with no known window resolves "auto" to off
// rather than guessing: compaction stays manual, the session still runs.
func ParseCompactSpec(spec string, window int) (CompactConfig, error) {
	switch spec {
	case "", "off":
		return CompactConfig{}, nil
	case "auto":
		if window <= 0 {
			return CompactConfig{}, nil
		}
		reserve := int64(window) / 20
		if reserve < CompactReserveFloor {
			reserve = CompactReserveFloor
		}
		return CompactConfig{Threshold: int64(window) - reserve}, nil
	}
	rest, ok := strings.CutPrefix(spec, "auto:")
	if !ok {
		return CompactConfig{}, fmt.Errorf(
			"-compact %q: want \"off\", \"auto\", \"auto:N%%\" or \"auto:N\" (a token count)", spec)
	}
	if pct, err := strconv.ParseInt(strings.TrimSuffix(rest, "%"), 10, 64); err == nil && strings.HasSuffix(rest, "%") {
		if window <= 0 {
			return CompactConfig{}, nil
		}
		if pct < 1 || pct > 99 {
			return CompactConfig{}, fmt.Errorf("-compact %q: the percentage must be between 1 and 99", spec)
		}
		return CompactConfig{Threshold: int64(window) * pct / 100}, nil
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || n < 1 {
		return CompactConfig{}, fmt.Errorf(
			"-compact %q: want \"off\", \"auto\", \"auto:N%%\" or \"auto:N\" (a token count)", spec)
	}
	return CompactConfig{Threshold: n}, nil
}

// SetCompact re-points the auto-compaction threshold, which a model switch has
// to do for the same reason SetContextEdit exists: "auto" is a function of the
// model's window and the catalogue spans 262K to 1M. Same rule as SetClient:
// between runs only, on the goroutine that owns the agent.
func (a *Agent) SetCompact(cfg CompactConfig) { a.compactCfg = cfg }

// CompactThreshold is the prompt size at which the driver should compact, or 0
// when compaction is manual. Safe to read from any goroutine for the same
// reason ContextEditTrigger is: assigned between runs by the owning goroutine.
func (a *Agent) CompactThreshold() int64 { return a.compactCfg.Threshold }

// ShouldCompact reports whether the conversation has crossed the auto-compaction
// threshold. Between runs only — the estimate it reads (promptTokens) shares the
// single-writer rule of every counter here.
//
// Two messages is the same floor Compact itself enforces: below it there is
// nothing a summary could be smaller than, and asking the summariser would be
// the one call a session with one exchange in it can waste.
func (a *Agent) ShouldCompact() bool {
	if a.compactCfg.Threshold <= 0 || len(a.messages) < 2 {
		return false
	}
	return a.promptTokens() >= a.compactCfg.Threshold
}
