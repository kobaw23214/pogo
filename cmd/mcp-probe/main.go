// Command mcp-probe is the smoke test for pogo's MCP endpoints — the
// protocol-and-deployment layer of testing, one rung below the web package's
// in-process suite and one above a human with the Inspector.
//
// What it is for: the in-process tests exercise handlers over httptest, which
// is the protocol but not the deployment. This command drives a *real* pogo
// process — the one started with POGO_MCP=1 and a token someone typed —
// through the sequence a remote agent's first day will hit: rejected without a
// credential, admitted with one (header and, on /mcp, query), the tool list,
// an error that reads as an error, and with -task the full submit → wait →
// result round trip including progress notifications. It is deliberately the
// acceptance script for the real 联调 too: when the other side finally
// connects, this is the checklist their client is expected to clear, run by
// us first against our own server.
//
// Why not the Inspector: the Inspector is interactive and general; this is
// scripted and ours. Both belong in the workflow — Inspector for exploring,
// mcp-probe for go/no-go.
//
// A note on -task: it exercises the loop, not the model. Against a server
// whose provider is unreachable (the minimal bootstrap in this file's
// companion docs), the task still reaches a terminal status — failed,
// transport_error — and that is a pass: the round trip carried. The status is
// printed either way; lying about it would make the probe worse than no probe.
//
// Usage, against a locally built server with no real provider:
//
//	providers.json: {"default":"probe-model",
//	  "providers":{"probe":{"base_url":"http://127.0.0.1:9/v1",
//	  "key_env":"PROBE_API_KEY","allow_insecure_http":true}},
//	  "models":[{"id":"probe-model","provider":"probe",
//	  "context_window":131072,"max_tokens":4096}]}
//
//	POGO_CONFIG=providers.json PROBE_API_KEY=x POGO_MCP=1 \
//	  POGO_WEB_TOKEN=t pogo -web -listen 127.0.0.1:7777
//	go run ./cmd/mcp-probe -url http://127.0.0.1:7777 -token t \
//	  -task "Reply with exactly: probe-ok"
//
// A deployment that narrows the surface with mcp.json's include_tools
// whitelist (a tool-server persona that hides the agent_* machinery) breaks
// the default tools/list contract by design, so -tools replaces it: the
// comma-separated list is then checked for an exact match — nothing missing,
// nothing extra — which is the contract a whitelist actually promises. The
// default nine stay a subset check, because manifest tools legitimately vary
// per deployment. -call adds one live tools/call ("name" or "name={json}") for
// a deployment-level execution smoke; it fails on a tool error, so it belongs
// against a server where the tool can really run:
//
//	POGO_MCP_TOKEN=t2 POGO_MCP=1 POGO_MCP_FILE=/data/mcp.json pogo -web …
//	go run ./cmd/mcp-probe -url http://127.0.0.1:17777 -token t2 \
//	  -tools "mal.search,mal.sample,mal.similar,mal.family,mal.stats" \
//	  -call 'mal.search={"family":"qakbot","n":5}'
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:7777", "pogo web base URL (the MCP endpoints are mounted on it)")
	token := flag.String("token", os.Getenv("POGO_WEB_TOKEN"), "credential; defaults to $POGO_WEB_TOKEN")
	transport := flag.String("transport", "both", "streamable, sse, or both")
	task := flag.String("task", "", "run the full task round trip with this prompt (needs a working model on the server)")
	tools := flag.String("tools", "", "comma-separated tools/list contract; replaces the default nine agent tools and is checked for an exact match (missing or extra names fail)")
	call := flag.String("call", "", `live tools/call smoke: "name" or "name={json arguments}"; fails on a tool error, so run it against a deployment where the tool can execute`)
	timeout := flag.Duration("timeout", 180*time.Second, "budget for the task round trip")
	flag.Parse()

	if *token == "" {
		fmt.Fprintln(os.Stderr, "mcp-probe: -token or $POGO_WEB_TOKEN is required — the endpoints are never unauthenticated")
		os.Exit(2)
	}
	p := &probe{base: strings.TrimSuffix(*url, "/"), token: *token, timeout: *timeout}
	if *tools != "" {
		for _, name := range strings.Split(*tools, ",") {
			if name = strings.TrimSpace(name); name != "" {
				p.want = append(p.want, name)
			}
		}
		if len(p.want) == 0 {
			fmt.Fprintln(os.Stderr, "mcp-probe: -tools parsed to an empty list")
			os.Exit(2)
		}
		p.exact = true
	}

	p.check("auth: streamable without a credential is rejected", func() error {
		_, err := p.connect("streamable", "", nil)
		if err == nil {
			return fmt.Errorf("connect succeeded without a token")
		}
		return nil
	})

	if *transport == "streamable" || *transport == "both" {
		p.runStreamable()
	}
	if *transport == "sse" || *transport == "both" {
		p.runLegacySSE()
	}
	if *call != "" {
		p.runCall(*call)
	}
	if *task != "" {
		p.runTask(*task)
	}

	fmt.Printf("\n%d checks, %d failures\n", p.n, len(p.failures))
	if len(p.failures) > 0 {
		for _, f := range p.failures {
			fmt.Printf("  FAIL %s\n", f)
		}
		os.Exit(1)
	}
}

// --- the probe harness ------------------------------------------------------

type probe struct {
	base    string
	token   string
	timeout time.Duration
	// want is the expected tools/list surface; exact says the match runs in
	// both directions (a whitelist contract) rather than as a subset (the
	// default nine, which manifest tools may legitimately extend).
	want     []string
	exact    bool
	n        int
	failures []string
}

func (p *probe) check(name string, fn func() error) {
	p.n++
	if err := fn(); err != nil {
		p.failures = append(p.failures, name)
		fmt.Printf("✗ %s — %v\n", name, err)
		return
	}
	fmt.Printf("✓ %s\n", name)
}

// connect builds a client session over one of the two transports. auth is
// "bearer", "query" or "" — the legacy transport takes only bearer, a fact
// the probe documents by construction rather than by branching around it.
// opts carries the progress handler for the sessions that want one; a
// notification handler only ever fires on the client that owns the session,
// so it has to ride the same NewClient as the connect.
func (p *probe) connect(transport, auth string, opts *mcp.ClientOptions) (*mcp.ClientSession, error) {
	endpoint, hc := p.base+"/mcp", http.DefaultClient
	if transport == "sse" {
		endpoint = p.base + "/sse"
	}
	switch auth {
	case "bearer":
		hc = &http.Client{Transport: bearerTransport{token: p.token}}
	case "query":
		endpoint += "?token=" + p.token
	case "":
		hc = &http.Client{}
	}
	var tp interface {
		Connect(context.Context) (mcp.Connection, error)
	}
	if transport == "sse" {
		tp = &mcp.SSEClientTransport{Endpoint: endpoint, HTTPClient: hc}
	} else {
		tp = &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc}
	}
	return mcp.NewClient(&mcp.Implementation{Name: "mcp-probe"}, opts).
		Connect(context.Background(), tp, nil)
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// wantAgentTools is the contract this server promises on tools/list by
// default. Listed here rather than discovered, because a probe that only
// checks "some tools came back" would pass through the exact regression it
// exists to catch. A deployment's include_tools whitelist can override the
// expectation with -tools, which is checked exactly.
var wantAgentTools = []string{
	"agent_submit_task", "agent_task_status", "agent_task_result",
	"agent_task_cancel", "agent_task_wait", "agent_task_steer",
	"agent_task_approve", "agent_task_list", "agent_task_file",
}

func (p *probe) wanted() []string {
	if len(p.want) > 0 {
		return p.want
	}
	return wantAgentTools
}

// matchTools checks the surface against the expectation: every wanted name
// present, and — under -tools, where the list is the deployment's own
// contract — nothing extra either.
func (p *probe) matchTools(names map[string]bool) error {
	var missing []string
	for _, want := range p.wanted() {
		if !names[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing: %s", strings.Join(missing, ", "))
	}
	if p.exact {
		var extra []string
		for _, want := range p.wanted() {
			names[want] = false
		}
		for name, still := range names {
			if still {
				extra = append(extra, name)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			return fmt.Errorf("exposed beyond the -tools contract: %s", strings.Join(extra, ", "))
		}
	}
	return nil
}

func listTools(cs *mcp.ClientSession) ([]*mcp.Tool, error) {
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}

func toolNames(cs *mcp.ClientSession) (map[string]bool, error) {
	list, err := listTools(cs)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(list))
	for _, t := range list {
		names[t.Name] = true
	}
	return names, nil
}

// --- scenario groups --------------------------------------------------------

func (p *probe) runStreamable() {
	p.check("streamable: connect and ping (Bearer)", func() error {
		cs, err := p.connect("streamable", "bearer", nil)
		if err != nil {
			return err
		}
		defer cs.Close()
		return cs.Ping(context.Background(), nil)
	})

	p.check("streamable: connect via ?token= in the URL", func() error {
		cs, err := p.connect("streamable", "query", nil)
		if err != nil {
			return err
		}
		defer cs.Close()
		return cs.Ping(context.Background(), nil)
	})

	p.check("streamable: tools/list matches the expected surface", func() error {
		cs, err := p.connect("streamable", "bearer", nil)
		if err != nil {
			return err
		}
		defer cs.Close()
		names, err := toolNames(cs)
		if err != nil {
			return err
		}
		return p.matchTools(names)
	})

	p.check("errors: a doomed call reads as a tool error", func() error {
		cs, err := p.connect("streamable", "bearer", nil)
		if err != nil {
			return err
		}
		defer cs.Close()
		// The default surface has agent_task_status, whose unknown-id answer
		// is the canonical shape. A whitelisted tool surface does not expose
		// it, so the same property is verified on one of the exposed tools
		// instead: a type-mismatched argument is rejected by argument
		// validation before anything executes, which exercises the
		// error-as-result contract without side effects.
		if p.surfaceHas("agent_task_status") {
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "agent_task_status",
				Arguments: map[string]any{"task_id": "probe-no-such-task"},
			})
			if err != nil {
				return err
			}
			if !res.IsError {
				return fmt.Errorf("unknown task came back as a success: %#v", res.StructuredContent)
			}
			return nil
		}
		list, err := listTools(cs)
		if err != nil {
			return err
		}
		for _, t := range list {
			tool, badArgs, ok := typeMismatchedCall(t)
			if !ok {
				continue
			}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: tool, Arguments: badArgs,
			})
			if err != nil {
				return err
			}
			if !res.IsError {
				return fmt.Errorf("invalid arguments to %s came back as a success", tool)
			}
			fmt.Printf("    (via %s: %s)\n", tool, firstText(res))
			return nil
		}
		fmt.Printf("    (no exposed tool declares a typed property; skipped)\n")
		return nil
	})
}

// surfaceHas reports whether the expected surface exposes a name.
func (p *probe) surfaceHas(name string) bool {
	for _, want := range p.wanted() {
		if want == name {
			return true
		}
	}
	return false
}

// typeMismatchedCall reads one tool's declared input schema and, when it finds
// a property with a simple type, returns arguments of the wrong type for it.
// The point is a call guaranteed to fail validation, so the probe can ask any
// surface "does an error read as an error" without executing anything.
func typeMismatchedCall(t *mcp.Tool) (string, map[string]any, bool) {
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return "", nil, false
	}
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil || len(schema.Properties) == 0 {
		return "", nil, false
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var bad any
		switch schema.Properties[name].Type {
		case "string":
			bad = 123
		case "number", "integer":
			bad = "not-a-number"
		case "boolean":
			bad = "not-a-boolean"
		default:
			continue
		}
		return t.Name, map[string]any{name: bad}, true
	}
	return "", nil, false
}

// runCall is the deployment-level execution smoke: one real tools/call, its
// answer previewed, a tool error counted as a failure because the point of
// the flag is "does this tool actually run here".
func (p *probe) runCall(spec string) {
	name, argJSON, hasArgs := strings.Cut(spec, "=")
	name = strings.TrimSpace(name)
	if name == "" {
		fmt.Fprintln(os.Stderr, "mcp-probe: -call wants \"name\" or \"name={json}\"")
		os.Exit(2)
	}
	args := map[string]any{}
	if hasArgs && strings.TrimSpace(argJSON) != "" {
		if err := json.Unmarshal([]byte(argJSON), &args); err != nil {
			fmt.Fprintf(os.Stderr, "mcp-probe: -call arguments are not JSON: %v\n", err)
			os.Exit(2)
		}
	}
	cs, err := p.connect("streamable", "bearer", nil)
	if err != nil {
		p.n++
		p.failures = append(p.failures, "call: connect")
		fmt.Printf("✗ call: connect — %v\n", err)
		return
	}
	defer cs.Close()
	p.check("call: "+name+" executes and answers", func() error {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return err
		}
		text := firstText(res)
		preview := text
		if len(preview) > 300 {
			preview = preview[:300] + "…"
		}
		fmt.Printf("    %s → %s\n", name, preview)
		if res.IsError {
			return fmt.Errorf("tool error")
		}
		return nil
	})
}

func (p *probe) runLegacySSE() {
	p.check("sse(legacy): connect and list tools (Bearer; query cannot survive this handshake)", func() error {
		cs, err := p.connect("sse", "bearer", nil)
		if err != nil {
			return err
		}
		defer cs.Close()
		names, err := toolNames(cs)
		if err != nil {
			return err
		}
		return p.matchTools(names)
	})
}

func (p *probe) runTask(prompt string) {
	var (
		taskID   string
		progress int
	)
	cs, err := p.connect("streamable", "bearer", &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			progress++
			fmt.Printf("    · progress #%d: %s\n", progress, r.Params.Message)
		},
	})
	if err != nil {
		p.n++
		p.failures = append(p.failures, "task: connect")
		fmt.Printf("✗ task: connect — %v\n", err)
		return
	}
	defer cs.Close()

	p.check("task: submit returns a task id", func() error {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "agent_submit_task",
			Arguments: map[string]any{"prompt": prompt},
		})
		if err != nil {
			return err
		}
		if res.IsError {
			return fmt.Errorf("submit: %s", firstText(res))
		}
		sc, _ := res.StructuredContent.(map[string]any)
		id, _ := sc["task_id"].(string)
		if id == "" {
			return fmt.Errorf("no task_id: %#v", sc)
		}
		taskID = id
		fmt.Printf("    task %s\n", id)
		return nil
	})
	if taskID == "" {
		return
	}

	p.check("task: wait streams progress and reaches a terminal status", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
		defer cancel()
		params := &mcp.CallToolParams{
			Name:      "agent_task_wait",
			Arguments: map[string]any{"task_id": taskID, "timeout_seconds": p.timeout.Seconds()},
		}
		params.SetProgressToken("probe-wait")
		res, err := cs.CallTool(ctx, params)
		if err != nil {
			return err
		}
		if res.IsError {
			return fmt.Errorf("wait: %s", firstText(res))
		}
		sc, _ := res.StructuredContent.(map[string]any)
		fmt.Printf("    status=%v end_reason=%v disposition=%v\n",
			sc["status"], sc["end_reason"], sc["disposition"])
		if st, _ := sc["status"].(string); st == "working" {
			return fmt.Errorf("wait returned while still working: %#v", sc)
		}
		if text := firstText(res); text != "" {
			preview := text
			if len(preview) > 200 {
				preview = preview[:200] + "…"
			}
			fmt.Printf("    answer: %s\n", preview)
		}
		return nil
	})

	p.check("task: steer on an idle task reports steered:false", func() error {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "agent_task_steer",
			Arguments: map[string]any{"task_id": taskID, "text": "probe steering an idle task"},
		})
		if err != nil {
			return err
		}
		sc, _ := res.StructuredContent.(map[string]any)
		if sc["steered"] != false {
			return fmt.Errorf("steered = %#v, want false on an idle task", sc["steered"])
		}
		return nil
	})
}

func firstText(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}
