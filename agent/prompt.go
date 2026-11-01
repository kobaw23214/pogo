package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// SystemPrompt builds the system prompt: a short role statement plus the
// environment facts the model cannot discover without spending a tool call.
// Kept deliberately short. Long prompts crowd out the actual task. Both system
// prompts and tool schemas cost tokens on every single turn. Tool schemas are
// ~5.8x larger than system prompts and should be optimized first.
//
// sections are appended verbatim, for things this package should not know how to
// build. skills.PromptSection is the first of them; keeping it a plain string is
// what keeps the agent package unaware that skills exist.
func SystemPrompt(cwd string, sections ...string) string {
	labeled := make([]PromptPart, len(sections))
	for i, s := range sections {
		labeled[i] = PromptPart{Key: PromptPartOther, Text: s}
	}
	return JoinPromptParts(SystemPromptParts(cwd, labeled...))
}

// PromptPart is one labeled share of a system prompt, kept so the fixed
// per-request cost can be itemized (see OverheadBreakdown). Text is exactly
// the bytes the part contributes, so the parts of a prompt concatenate to it.
// Key is a stable identifier that interfaces localize.
type PromptPart struct {
	Key  string
	Text string
}

// Stable prompt part keys. The first group is what SystemPromptParts and the
// web host label; the second is what the agent appends itself (mcpguide.go).
const (
	PromptPartBase      = "base"
	PromptPartProject   = "project"
	PromptPartUI        = "ui"
	PromptPartSkills    = "skills"
	PromptPartMemory    = "memory"
	PromptPartCitations = "citations"
	PromptPartAgent     = "agent"
	PromptPartRuntime   = "runtime"
	PromptPartOther     = "other"

	PromptPartBackground  = "background"
	PromptPartMCPDeferred = "mcp_deferred"
	PromptPartSkillTools  = "skill_tools"
)

// JoinPromptParts is the prompt a list of parts describes.
func JoinPromptParts(parts []PromptPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// SystemPromptParts is SystemPrompt with its labeled shares: the role and
// environment, the project conventions file, then each non-empty section in
// order. JoinPromptParts of the result is byte-for-byte SystemPrompt of the
// same texts; empty sections contribute nothing and get no part.
func SystemPromptParts(cwd string, sections ...PromptPart) []PromptPart {
	var b strings.Builder
	b.WriteString(`You are pogo, an agent working with the tools available in this session.

For code tasks, read files before changing them, and prefer edit over write so
you touch only the lines that need to change. Verify changes with the project's
build, tests or lint when those tools are available.

Each tool call is a round trip, so do independent work in one: several files in a
single read with paths, and independent calls together in one message. Anything
whose arguments depend on another call's output has to wait for it.

Be concise and answer in the user's language, with enough detail for the requested
deliverable. Your final answer must stand on its own: include the requested report
or result, or a direct link to an artifact you actually saved. An earlier progress
message or "see above" is not the final deliverable. For code changes, summarize
what changed and how it was checked instead of restating the diff.

Reuse successful results already obtained. Repeat a call only for a stated need,
such as a changed scope or a required freshness check. A tool failure is evidence:
read it and choose a justified next step; do not blindly retry or replay a call
that may have side effects. Respect the user's total sample, page, cost and action
limits across the task, including retries. When a limit or unsupported scope
prevents more evidence, report the gap instead of silently widening the request.
Do not delete files, reset version control, or touch anything outside the working
directory unless the user asked for it. If you commit, stage the paths you
changed rather than everything, and never bypass hooks with --no-verify or
rewrite history with amend, reset or force-push.

Mid-run input supplements work unless it cancels or replaces it. Answer questions,
apply corrections, then resume unfinished work.
`)
	// The batching paragraph is about the loop, which is why it is here and not in a
	// tool description: it is the one instruction that no single tool owns, and a
	// session's tools can all be present without anything telling the model that a
	// turn may carry more than one call. What belongs to a tool stays with the tool —
	// read's paths, and bash's non-persistent shell, are documented on those two and
	// nowhere else, so a session without them is never told about them.
	//
	// Costed rather than assumed, because this file's own header says tool schemas
	// are ~5.8x the system prompt and are what to optimise first: the paragraph is
	// 226 bytes on a prompt of ~1.1KB, against 4785 bytes of schemas. Three sentences
	// is the budget; the third one exists because "batch everything" is worse advice
	// than none — a call whose arguments come from another call's output cannot be
	// batched, and a model that tries produces a turn's worth of wrong arguments.
	//
	// Nothing here mentions the task list on purpose. That guidance lives in the
	// todo tool's own description, which is loaded only when the tool is registered
	// — so a subagent child, which does not get the tool, is never told to keep a
	// list it has no way to write. A line here would have to be made conditional on
	// the registry, which this function deliberately knows nothing about.
	fmt.Fprintf(&b, "\n<env>\nworking directory: %s\nplatform: %s/%s\ndate: %s\n",
		cwd, runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02"))
	if shell := os.Getenv("SHELL"); shell != "" {
		fmt.Fprintf(&b, "shell: %s\n", shell)
	}
	b.WriteString("</env>\n")
	parts := []PromptPart{{Key: PromptPartBase, Text: b.String()}}

	// Project conventions, same idea as pi reading AGENTS.md.
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		parts = append(parts, PromptPart{Key: PromptPartProject,
			Text: fmt.Sprintf("\n<%s>\n%s\n</%s>\n", name, strings.TrimSpace(string(data)), name)})
		break
	}

	for _, s := range sections {
		if text := strings.TrimSpace(s.Text); text != "" {
			key := s.Key
			if key == "" {
				key = PromptPartOther
			}
			parts = append(parts, PromptPart{Key: key, Text: "\n" + text + "\n"})
		}
	}
	return parts
}

// labeledSystemPrompt keeps a caller's labels only when they describe this
// exact prompt. Anything else, including no labels at all, is one base part:
// a stale label must never itemize text it does not cover.
func labeledSystemPrompt(prompt string, parts []PromptPart) []PromptPart {
	if len(parts) > 0 && JoinPromptParts(parts) == prompt {
		out := make([]PromptPart, 0, len(parts))
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p)
			}
		}
		return out
	}
	if prompt == "" {
		return nil
	}
	return []PromptPart{{Key: PromptPartBase, Text: prompt}}
}
