package observability

// A bounded OTLP/HTTP JSON exporter for the existing metadata event streams.
// It does not collect arguments, results, paths, credentials, or raw errors.
// Configuration is explicit; an empty endpoint performs no network activity.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
)

type OTLPOptions struct {
	Endpoint, ServiceName, InstanceID string
	Headers                           map[string]string
	QueueSize, BatchSize              int
	Timeout, FlushInterval            time.Duration
	// RetryLimit excludes the first attempt; zero uses the conservative default.
	RetryLimit int
}

type OTLPStats struct {
	Enabled       bool   `json:"enabled"`
	Protocol      string `json:"protocol,omitempty"`
	Queued        int    `json:"queued"`
	QueueCapacity int    `json:"queue_capacity"`
	Exported      int64  `json:"exported"`
	Failed        int64  `json:"failed"`
	Dropped       int64  `json:"dropped"`
	Rejected      int64  `json:"rejected"`
	SampledOut    int64  `json:"sampled_out"`
	Invalid       int64  `json:"invalid"`
	LastErrorKind string `json:"last_error_kind,omitempty"`
	LastSuccessAt int64  `json:"last_success_at,omitempty"`
}

type OTLPExporter struct {
	options                                                  OTLPOptions
	client                                                   *http.Client
	queue                                                    chan otlpSpan
	ctx                                                      context.Context
	cancel                                                   context.CancelFunc
	done                                                     chan struct{}
	mu                                                       sync.RWMutex
	closed                                                   bool
	once                                                     sync.Once
	exported, failed, dropped, rejected, sampledOut, invalid atomic.Int64
	lastSuccess                                              atomic.Int64
	lastError                                                atomic.Value // a category only, never an HTTP response or URL
}

func NewOTLPExporter(o OTLPOptions) (*OTLPExporter, error) {
	if strings.TrimSpace(o.Endpoint) == "" {
		return nil, nil
	}
	u, err := url.Parse(o.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("OTLP endpoint must be an HTTP(S) URL without user info or fragment")
	}
	if o.QueueSize == 0 {
		o.QueueSize = 2048
	}
	if o.BatchSize == 0 {
		o.BatchSize = 128
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Second
	}
	if o.FlushInterval == 0 {
		o.FlushInterval = time.Second
	}
	if o.RetryLimit == 0 {
		o.RetryLimit = 2
	}
	if o.QueueSize < 1 || o.QueueSize > 65536 || o.BatchSize < 1 || o.BatchSize > 1024 || o.Timeout < time.Millisecond || o.Timeout > time.Minute || o.FlushInterval < time.Millisecond || o.FlushInterval > time.Minute || o.RetryLimit < 0 || o.RetryLimit > 5 {
		return nil, errors.New("OTLP limits are outside supported bounds")
	}
	if o.ServiceName == "" {
		o.ServiceName = "pogo"
	}
	headers := make(map[string]string, len(o.Headers))
	for key, value := range o.Headers {
		if !otlpHeaderName(key) || strings.ContainsAny(value, "\r\n\x00") || len(value) > 8192 {
			return nil, errors.New("invalid OTLP header configuration")
		}
		switch strings.ToLower(key) {
		case "host", "content-length", "content-type", "transfer-encoding", "connection":
			return nil, errors.New("OTLP transport headers cannot be overridden")
		}
		key = http.CanonicalHeaderKey(key)
		if _, exists := headers[key]; exists {
			return nil, errors.New("duplicate OTLP header")
		}
		headers[key] = value
	}
	o.Headers = headers
	x := &OTLPExporter{options: o, queue: make(chan otlpSpan, o.QueueSize), done: make(chan struct{})}
	x.ctx, x.cancel = context.WithCancel(context.Background())
	x.client = &http.Client{Timeout: o.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	go x.run()
	return x, nil
}

func (x *OTLPExporter) RecordMCP(c mcpobserve.Call) {
	if x == nil || c.Status == "running" {
		return
	}
	kind := 1
	if c.Direction == "inbound" {
		kind = 2
	} else if c.Direction == "outbound" {
		kind = 3
	}
	s, ok := otlpNewSpan(c.ID, c.ParentID, c.TraceID, c.SpanID, c.ParentSpanID, c.TraceFlags, c.StartedAt, c.FinishedAt, c.DurationMs, "tools/call "+c.Tool, kind, c.Status)
	if !ok {
		x.invalid.Add(1)
		return
	}
	s.Attributes = otlpAttributes(map[string]string{
		"mcp.method.name": "tools/call", "mcp.tool.name": c.Tool,
		"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": c.Tool, "gen_ai.tool.type": "function",
		"pogo.event.id": c.ID, "pogo.event.parent_id": c.ParentID, "pogo.direction": c.Direction,
		"pogo.server": c.Server, "pogo.tool.wire_name": c.WireTool, "pogo.transport": c.Transport,
		"pogo.client.name": c.Client, "pogo.client.id": c.ClientID,
		"pogo.entrypoint": c.Entrypoint, "pogo.agent.id": c.AgentID, "pogo.agent.name": c.AgentName, "pogo.session.id": c.SessionID, "pogo.run.id": c.RunID,
		"pogo.task.id": c.TaskID, "pogo.tool_call.id": c.ToolCallID, "pogo.status": c.Status,
		"error.type": otlpErrorKind(c.ErrorKind),
	})
	x.record(s)
}

func (x *OTLPExporter) RecordSkill(e skillobserve.Event) {
	if x == nil || e.Status == "running" {
		return
	}
	s, ok := otlpNewSpan(e.ID, e.ParentID, e.TraceID, e.SpanID, e.ParentSpanID, e.TraceFlags, e.StartedAt, e.FinishedAt, e.DurationMs, "skill."+e.Stage+" "+e.SkillName, 1, e.Status)
	if !ok {
		x.invalid.Add(1)
		return
	}
	attrs := map[string]string{
		"pogo.event.id": e.ID, "pogo.event.parent_id": e.ParentID,
		"pogo.skill.id": e.SkillID, "pogo.skill.name": e.SkillName, "pogo.skill.source": e.SkillSource,
		"pogo.skill.stage": e.Stage, "pogo.skill.mechanism": e.Mechanism,
		"pogo.caller": e.Caller, "pogo.client.id": e.ClientID, "pogo.entrypoint": e.Entrypoint,
		"pogo.session.id": e.SessionID, "pogo.run.id": e.RunID, "pogo.task.id": e.TaskID,
		"pogo.tool_call.id": e.ToolCallID, "pogo.tool.wire_name": e.WireTool, "pogo.status": e.Status,
		"error.type": otlpErrorKind(e.ErrorKind),
	}
	if e.Stage == "execute" {
		attrs["gen_ai.operation.name"], attrs["gen_ai.tool.name"], attrs["gen_ai.tool.type"] = "execute_tool", e.Tool, "function"
	}
	s.Attributes = otlpAttributes(attrs)
	x.record(s)
}

func (x *OTLPExporter) record(s otlpSpan) {
	if s.Flags&1 == 0 {
		x.sampledOut.Add(1)
		return
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	if x.closed {
		x.dropped.Add(1)
		return
	}
	select {
	case x.queue <- s:
	default:
		x.dropped.Add(1)
	}
}

func (x *OTLPExporter) Stats() OTLPStats {
	if x == nil {
		return OTLPStats{}
	}
	kind, _ := x.lastError.Load().(string)
	return OTLPStats{Enabled: true, Protocol: "http/json", Queued: len(x.queue), QueueCapacity: cap(x.queue), Exported: x.exported.Load(), Failed: x.failed.Load(), Dropped: x.dropped.Load(), Rejected: x.rejected.Load(), SampledOut: x.sampledOut.Load(), Invalid: x.invalid.Load(), LastErrorKind: kind, LastSuccessAt: x.lastSuccess.Load()}
}

func (x *OTLPExporter) Shutdown(ctx context.Context) error {
	if x == nil {
		return nil
	}
	x.once.Do(func() { x.mu.Lock(); x.closed = true; close(x.queue); x.mu.Unlock() })
	select {
	case <-x.done:
		return nil
	case <-ctx.Done():
		x.cancel()
		return ctx.Err()
	}
}

func (x *OTLPExporter) run() {
	defer close(x.done)
	defer x.cancel()
	ticker := time.NewTicker(x.options.FlushInterval)
	defer ticker.Stop()
	batch := make([]otlpSpan, 0, x.options.BatchSize)
	flush := func() {
		if len(batch) > 0 {
			x.export(batch)
			batch = batch[:0]
		}
	}
	for {
		select {
		case s, ok := <-x.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, s)
			if len(batch) >= x.options.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-x.ctx.Done():
			x.failed.Add(int64(len(batch)))
			for range x.queue {
				x.failed.Add(1)
			}
			return
		}
	}
}

func (x *OTLPExporter) export(spans []otlpSpan) {
	body, err := json.Marshal(otlpEnvelope{ResourceSpans: []otlpResourceSpans{{
		Resource:   otlpResource{Attributes: otlpAttributes(map[string]string{"service.name": x.options.ServiceName, "service.instance.id": x.options.InstanceID})},
		ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: "pogo.observability", Version: "1"}, Spans: spans}},
	}}})
	if err != nil {
		x.failed.Add(int64(len(spans)))
		x.lastError.Store("encoding_error")
		return
	}
	for attempt := 0; ; attempt++ {
		rejected, retry, delay, kind := x.send(body, len(spans))
		if kind == "" || kind == "partial_success" {
			x.exported.Add(int64(len(spans) - rejected))
			x.rejected.Add(int64(rejected))
			x.lastSuccess.Store(time.Now().UnixMilli())
			x.lastError.Store(kind)
			return
		}
		x.lastError.Store(kind)
		if !retry || attempt >= x.options.RetryLimit || x.ctx.Err() != nil {
			x.failed.Add(int64(len(spans)))
			return
		}
		if delay <= 0 {
			delay = time.Duration(1<<attempt) * 100 * time.Millisecond
			delay += time.Duration(rand.Int64N(int64(delay/4) + 1))
		}
		// Server Retry-After is a minimum, never shorten it and retry early.
		// Excessive waits exceed this bounded best-effort exporter budget.
		if delay > 30*time.Second {
			x.failed.Add(int64(len(spans)))
			return
		}
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-x.ctx.Done():
			t.Stop()
			x.failed.Add(int64(len(spans)))
			return
		}
	}
}

func (x *OTLPExporter) send(body []byte, size int) (rejected int, retry bool, delay time.Duration, kind string) {
	ctx, cancel := context.WithTimeout(x.ctx, x.options.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.options.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, false, 0, "configuration_error"
	}
	for key, value := range x.options.Headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := x.client.Do(req)
	if err != nil {
		if x.ctx.Err() != nil {
			return 0, false, 0, "cancelled"
		}
		if ctx.Err() != nil {
			return 0, true, 0, "timeout"
		}
		return 0, true, 0, "connection_error"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		retry = resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
		if value := resp.Header.Get("Retry-After"); value != "" {
			if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
				delay = time.Duration(seconds) * time.Second
			} else if at, err := http.ParseTime(value); err == nil {
				delay = time.Until(at)
			}
		}
		return 0, retry, delay, "http_" + strconv.Itoa(resp.StatusCode)
	}
	// Limit after HTTP's transparent decompression. Never log response text.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return 0, true, 0, "response_read_error"
	}
	if len(raw) > 4<<20 {
		return 0, false, 0, "response_too_large"
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false, 0, "invalid_response"
	}
	var result struct {
		Partial *struct {
			Rejected json.Number `json:"rejectedSpans"`
			Message  string      `json:"errorMessage"`
		} `json:"partialSuccess"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, false, 0, "invalid_response"
	}
	if result.Partial != nil {
		var n int64
		if result.Partial.Rejected != "" {
			n, err = result.Partial.Rejected.Int64()
		}
		if err != nil || n < 0 || n > int64(size) {
			return 0, false, 0, "invalid_response"
		}
		if n > 0 || result.Partial.Message != "" {
			return int(n), false, 0, "partial_success"
		}
	}
	return 0, false, 0, ""
}

func otlpHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r > 127 || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}

func otlpHex(s string, n int) bool {
	if len(s) != n || strings.Trim(s, "0") == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func otlpNewSpan(id, parent, trace, span, parentSpan, flags string, started, finished int64, duration float64, name string, kind int, status string) (otlpSpan, bool) {
	if span == "" {
		span = mcpobserve.SpanIDFor(id)
	}
	if parentSpan == "" {
		parentSpan = mcpobserve.SpanIDFor(parent)
	}
	if flags == "" {
		flags = "01"
	}
	f, err := strconv.ParseUint(flags, 16, 8)
	if !otlpHex(trace, 32) || !otlpHex(span, 16) || parentSpan != "" && !otlpHex(parentSpan, 16) || err != nil || len(flags) != 2 || started <= 0 || started > math.MaxInt64/int64(time.Millisecond) || finished <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 {
		return otlpSpan{}, false
	}
	startNS := started * int64(time.Millisecond)
	deltaNS := duration * float64(time.Millisecond)
	if deltaNS > float64(math.MaxInt64-startNS) {
		return otlpSpan{}, false
	}
	endNS := startNS + int64(deltaNS)
	code := 0
	switch status {
	case "ok":
		code = 1
	case "error", "timeout", "interrupted":
		code = 2
	case "cancelled":
	default:
		return otlpSpan{}, false
	}
	return otlpSpan{TraceID: trace, SpanID: span, ParentSpanID: parentSpan, Flags: uint32(f), Name: otlpText(name), Kind: kind, Start: strconv.FormatInt(startNS, 10), End: strconv.FormatInt(endNS, 10), Status: otlpStatus{Code: code}}, true
}

func otlpText(value string) string {
	runes := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value))
	if len(runes) > 256 {
		runes = runes[:256]
	}
	return string(runes)
}

func otlpErrorKind(kind string) string {
	switch kind {
	case "", "tool_error", "timeout", "cancelled", "protocol_error", "connection_error", "transport_error", "invalid_outcome", "unknown_outcome", "execution_error", "validation_error", "read_error", "configuration_error", "permission_error", "internal_error", "rejected", "unknown_error":
		return kind
	default:
		return "unknown_error"
	}
}

func otlpAttributes(values map[string]string) []otlpAttribute {
	attrs := make([]otlpAttribute, 0, len(values))
	for k, v := range values {
		if v != "" {
			attrs = append(attrs, otlpAttribute{Key: k, Value: otlpValue{String: otlpText(v)}})
		}
	}
	return attrs
}

// OTLP JSON uses lowerCamelCase, numeric enums, hex IDs and decimal strings
// for 64-bit timestamps. This is not Go's ordinary struct JSON representation.
type otlpEnvelope struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}
type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}
type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}
type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}
type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type otlpAttribute struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}
type otlpValue struct {
	String string `json:"stringValue"`
}
type otlpStatus struct {
	Code int `json:"code"`
}
type otlpSpan struct {
	TraceID      string          `json:"traceId"`
	SpanID       string          `json:"spanId"`
	ParentSpanID string          `json:"parentSpanId,omitempty"`
	Flags        uint32          `json:"flags"`
	Name         string          `json:"name"`
	Kind         int             `json:"kind"`
	Start        string          `json:"startTimeUnixNano"`
	End          string          `json:"endTimeUnixNano"`
	Attributes   []otlpAttribute `json:"attributes"`
	Status       otlpStatus      `json:"status"`
}
