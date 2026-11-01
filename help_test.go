package main

import (
	"testing"
)

// commandWord screens a one-shot prompt. The trap it exists to close: -p is the
// scripted entry point, and without this `-p /compact` sent the literal text to the
// model, which would answer it as a question inside the conversation it was meant
// to replace. The /skill: expansion beside it was fixed for the same reason.
func TestCommandWordScreensOneShotPrompts(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/compact", "/compact"},
		{"/usage", "/usage"},
		{"  /compact  ", "/compact"},
		// A prompt that merely begins with a slash is a prompt. This is the case that
		// makes exact-word matching necessary rather than a prefix check.
		{"/usr/local/bin is on PATH?", ""},
		{"/compacting is a word", ""},
		{"what does /compact do?", ""},
		{"/nope", ""},
		{"hello", ""},
		{"", ""},
		// No prefix expansion: a script has no completion list to have shown what a
		// guess would become, so a guess must not become anything.
		{"/comp", ""},
		{"/c", ""},
	} {
		if got := commandWord(c.in); got != c.want {
			t.Errorf("commandWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every listed command is screened, not just the ones spelled out above — so a
// word added to the list later cannot leak through -p as a literal prompt.
func TestEveryCommandIsScreenedFromOneShotPrompts(t *testing.T) {
	for _, c := range slashCommands {
		if got := commandWord(c); got != c {
			t.Errorf("%s is not screened: commandWord returned %q", c, got)
		}
	}
}
