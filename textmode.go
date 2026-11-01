package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/yosukeno/pogo/agent"
)

// textEmitter is the -mode text front end. When a run ends it prints the run's
// final answer, the last assistant message that had any text, and nothing else.
//
// It replaced the interactive terminal UI (status dock, live markdown, tool
// output). The browser UI is the interactive front end now; this exists so that
// `pogo -p "..."` and `... | pogo -p "..."` stay usable from a shell or a script.
//
// The settled message is printed rather than the streamed deltas, because the
// loop can still rewrite an answer after streaming it (citation and quote
// repair), and the settled text is what the session file keeps. The text is
// written as-is apart from a guaranteed final newline: piped output must not
// gain bytes the model did not write.
type textEmitter struct {
	out io.Writer
}

func newTextEmitter(out io.Writer) *textEmitter {
	return &textEmitter{out: out}
}

// Consume drains one run's events and returns the run's error, matching
// jsonEmitter: the exit status of -p must not depend on the output mode.
func (t *textEmitter) Consume(events <-chan agent.Event) error {
	var answer string
	var runErr error
	// The channel is always drained to the end: the loop blocks on its
	// terminating send, and the session flush that follows needs the run over.
	for e := range events {
		switch e.Kind {
		case agent.EventMessage:
			if text := e.Message.Text(); strings.TrimSpace(text) != "" {
				answer = text
			}
		case agent.EventAgentEnd:
			runErr = e.Err
		}
	}
	if answer == "" {
		return runErr
	}
	if !strings.HasSuffix(answer, "\n") {
		answer += "\n"
	}
	if _, err := io.WriteString(t.out, answer); err != nil && runErr == nil {
		// A broken pipe (`| head`) is not the run's fault, but it is still the
		// reason the answer did not arrive, so it is the error when the run had none.
		return fmt.Errorf("writing answer: %w", err)
	}
	return runErr
}
