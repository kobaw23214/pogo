package jsonfix

// Package jsonfix repairs malformed JSON in model output — the field failure
// of endpoints that answer in JSON but break it: a missing bracket, single
// quotes, a stray comma.
//
// Its scope is deliberately half of the problem. Free-text answers where
// nothing executes may be repaired as a fallback once the strict parse has
// failed; tool-call arguments may not, and pogo keeps passing those through
// untouched so the tool's own decoder produces the error the model reads and
// re-issues. That split is not pogo's invention: every production agent
// surveyed (Codex, Claude Code, Gemini CLI, the Vercel AI SDK) errors
// arguments back to the model rather than repair them, because a guessed
// completion of a command is riskier than a wasted turn — and repair applied
// to a token-capped stream would dress truncation up as an answer.
//
// The two rules the callers cannot state, this package enforces: repair is a
// fallback, never a first attempt (valid input returns untouched), and it is
// bounded (oversized input is refused — repair is for prose-sized answers,
// not payloads). The output is re-validated so a library bug cannot smuggle
// invalid JSON past a caller that was promised validity.

import (
	"encoding/json"

	"github.com/kaptinlin/jsonrepair"
)

// Budget caps what may be fed to the repairer, mirroring the differ's input
// cap: answers sized like payloads are someone else's problem, and an
// unbounded walk over megabytes of model text is a latency bug wearing a
// correctness hat.
const Budget = 2 << 20

// Repair returns s untouched when s is already valid JSON, and otherwise the
// best repaired form it can stand behind. repaired says which: false means
// "nothing changed" — valid input, over-budget input, or damage the repairer
// would not guess at.
func Repair(s string) (fixed string, repaired bool) {
	if json.Valid([]byte(s)) {
		return s, false
	}
	if len(s) > Budget {
		return s, false
	}
	out, err := jsonrepair.Repair(s)
	if err != nil || !json.Valid([]byte(out)) {
		return s, false
	}
	return out, true
}
