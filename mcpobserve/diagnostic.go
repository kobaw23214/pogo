package mcpobserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Diagnostic is a display-only, untrusted failure explanation, never a payload.
// Only Summary and Code pass into local history; neither is exported to OTLP.
type Diagnostic struct{ Summary, Code string }

const diagnosticLimit = 512
const diagnosticInputLimit = 64 * 1024

var (
	diagnosticURL        = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s<>"']+`)
	diagnosticAuth       = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[^\s,;<>"']+`)
	diagnosticHeader     = regexp.MustCompile(`(?im)\b(?:authorization|proxy-authorization|cookie|set-cookie)\s*:\s*[^\r\n]+`)
	diagnosticCredential = regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization|password|passwd|pwd|token|access[_-]?token|refresh[_-]?token|id[_-]?token|api[_-]?key|secret|client[_-]?secret|cookie|set-cookie|credential|session[_-]?key)\b["']?\s*[:=]\s*)("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^,;\r\n\}]+)`)
	diagnosticQuoted     = regexp.MustCompile(`"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'`)
	diagnosticJWT        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	diagnosticOpaque     = regexp.MustCompile(`[A-Za-z0-9_+/=-]{40,}`)
	diagnosticKey        = regexp.MustCompile(`\b(?:sk|ghp|github_pat|xox[baprs])[-_][A-Za-z0-9_-]+\b`)
	diagnosticSQLLabel   = regexp.MustCompile(`(?im)\b(?:sql|query|statement|parameters|params|arguments|request[_ ]?body|response[_ ]?body)\s*[:=][^\r\n]*`)
	diagnosticSQL        = regexp.MustCompile(`(?is)\b(?:SELECT\s+.+?\s+FROM\s+|INSERT\s+INTO\s+|UPDATE\s+[^\s]+\s+SET\s+|DELETE\s+FROM\s+|ALTER\s+TABLE\s+|DROP\s+TABLE\s+).*$`)
	diagnosticCode       = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	diagnosticTextCode   = regexp.MustCompile(`(?s)^([a-z][a-z0-9]*(?:_[a-z0-9]+)+):\s*(.+)$`)
)

// SafeDiagnostic is the final local-storage boundary, also used for direct
// recorder callers. Sanitize before truncating so a long credential cannot leak
// its prefix. Credentials, quoted values, SQL, URLs and opaque tokens are not
// diagnostic metadata. Request-aware helpers additionally remove echoed inputs.
func SafeDiagnostic(d Diagnostic) Diagnostic {
	s := d.Summary
	if len(s) > diagnosticInputLimit {
		s = s[:diagnosticInputLimit]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if unicode.IsControl(r) {
			if r == '\n' || r == '\r' || r == '\t' {
				return r
			}
			return -1
		}
		return r
	}, s)
	// Never retain an HTML response body as the explanation.
	lower := strings.ToLower(s)
	for _, marker := range []string{"<!doctype", "<html", "<body", "<script"} {
		if i := strings.Index(lower, marker); i >= 0 {
			s = s[:i]
			lower = strings.ToLower(s)
		}
	}
	s = diagnosticURL.ReplaceAllString(s, "[URL omitted]")
	s = diagnosticAuth.ReplaceAllString(s, "[credential redacted]")
	s = diagnosticHeader.ReplaceAllString(s, "[credential header redacted]")
	s = diagnosticCredential.ReplaceAllString(s, "${1}REDACTED")
	s = diagnosticSQLLabel.ReplaceAllString(s, "[request data omitted]")
	s = diagnosticSQL.ReplaceAllString(s, "[SQL omitted]")
	s = diagnosticJWT.ReplaceAllString(s, "[credential redacted]")
	s = diagnosticKey.ReplaceAllString(s, "[credential redacted]")
	s = diagnosticQuoted.ReplaceAllString(s, "[value redacted]")
	s = diagnosticOpaque.ReplaceAllString(s, "[opaque value redacted]")
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > diagnosticLimit {
		s = string(runes[:diagnosticLimit-1]) + "…"
	}
	code := strings.TrimSpace(d.Code)
	if !diagnosticCode.MatchString(code) || diagnosticKey.MatchString(code) || diagnosticJWT.MatchString(code) || diagnosticOpaque.MatchString(code) {
		code = ""
	}
	return Diagnostic{Summary: s, Code: code}
}

// DiagnoseError uses the observed error, not arguments/results serialization.
// RPC Data is deliberately ignored: it commonly contains the original request.
func DiagnoseError(err error, args json.RawMessage) Diagnostic {
	if err == nil {
		return Diagnostic{}
	}
	d := Diagnostic{Summary: err.Error()}
	var rpc *jsonrpc.Error
	switch {
	case errors.As(err, &rpc):
		d.Summary = rpc.Message
		d.Code = "rpc_" + strconv.FormatInt(rpc.Code, 10)
	case errors.Is(err, context.DeadlineExceeded):
		d.Code = "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		d.Code = "cancelled"
	}
	return redactDiagnosticInputs(d, args)
}

// DiagnoseResult extracts only explicitly labelled diagnostic fields from an
// isError result. Structured data/rows, arbitrary nested objects, rich content,
// and success output never enter the call history.
func DiagnoseResult(result *mcp.CallToolResult, args json.RawMessage) Diagnostic {
	if result == nil || !result.IsError {
		return Diagnostic{}
	}
	d := diagnosticObject(result.StructuredContent, 0)
	for _, block := range result.Content {
		text, ok := block.(*mcp.TextContent)
		if !ok || text == nil || strings.TrimSpace(text.Text) == "" {
			continue
		}
		next := diagnosticText(text.Text)
		if d.Summary == "" {
			d.Summary = next.Summary
		}
		if d.Code == "" {
			d.Code = next.Code
		}
		if d.Summary != "" && d.Code != "" {
			break
		}
	}
	return redactDiagnosticInputs(d, args)
}

func diagnosticText(text string) Diagnostic {
	if len(text) > diagnosticInputLimit {
		text = text[:diagnosticInputLimit]
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		var value any
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if json.Valid([]byte(text)) && decoder.Decode(&value) == nil {
			return diagnosticObject(value, 0)
		}
		// A malformed JSON response is still useful as a protocol failure, but not
		// safe to retain as partial request/result data.
		return Diagnostic{Summary: "MCP tool returned malformed error JSON", Code: "malformed_error_json"}
	}
	// YX and other MCP servers use the explicit snake_case code: message error
	// envelope. A prose prefix such as "Error:" is not promoted to a code.
	if parts := diagnosticTextCode.FindStringSubmatch(text); parts != nil {
		return Diagnostic{Code: parts[1], Summary: parts[2]}
	}
	return Diagnostic{Summary: text}
}

func diagnosticObject(value any, depth int) Diagnostic {
	if depth > 3 {
		return Diagnostic{}
	}
	switch raw := value.(type) {
	case json.RawMessage:
		if len(raw) > diagnosticInputLimit || !json.Valid(raw) {
			return Diagnostic{}
		}
		var parsed any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&parsed) != nil {
			return Diagnostic{}
		}
		return diagnosticObject(parsed, depth+1)
	case map[string]any:
		// Only the error envelope itself may recurse, never data/result/rows/args.
		d := Diagnostic{}
		if nested, ok := raw["error"].(map[string]any); ok {
			d = diagnosticObject(nested, depth+1)
		}
		if d.Code == "" {
			for _, key := range []string{"code", "error_code"} {
				switch v := raw[key].(type) {
				case string:
					d.Code = v
				case json.Number:
					d.Code = string(v)
				case float64:
					if v == float64(int64(v)) {
						d.Code = strconv.FormatInt(int64(v), 10)
					}
				case int:
					d.Code = strconv.Itoa(v)
				case int64:
					d.Code = strconv.FormatInt(v, 10)
				}
				if d.Code != "" {
					break
				}
			}
		}
		if d.Summary == "" {
			for _, key := range []string{"message", "msg", "reason", "detail", "error_description", "error"} {
				if v, ok := raw[key].(string); ok && strings.TrimSpace(v) != "" {
					d.Summary = v
					break
				}
			}
		}
		return d
	default:
		return Diagnostic{}
	}
}

// Echoed values are removed even if the server forgot to label them as a
// secret. This reads bounded JSON solely for redaction; values are never saved.
func redactDiagnosticInputs(d Diagnostic, args json.RawMessage) Diagnostic {
	if len(args) > diagnosticInputLimit {
		return SafeDiagnostic(Diagnostic{Code: d.Code, Summary: "MCP call failed; detailed text omitted because request values could not be safely redacted"})
	}
	if len(args) != 0 {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(args))
		decoder.UseNumber()
		if !json.Valid(args) || decoder.Decode(&value) != nil {
			return SafeDiagnostic(Diagnostic{Code: d.Code, Summary: "MCP call failed; detailed text omitted because request values could not be safely redacted"})
		}
		values := map[string]bool{}
		unsafeDepth := false
		var walk func(any, int)
		walk = func(v any, depth int) {
			if depth > 24 {
				unsafeDepth = true
				return
			}
			switch v := v.(type) {
			case string:
				if v != "" {
					values[v] = true
				}
			case json.Number:
				values[string(v)] = true
			case map[string]any:
				for _, child := range v {
					walk(child, depth+1)
				}
			case []any:
				for _, child := range v {
					walk(child, depth+1)
				}
			}
		}
		walk(value, 0)
		if unsafeDepth {
			return SafeDiagnostic(Diagnostic{Code: d.Code, Summary: "MCP call failed; detailed text omitted because request values could not be safely redacted"})
		}
		ordered := make([]string, 0, len(values))
		for v := range values {
			ordered = append(ordered, v)
		}
		sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
		for _, v := range ordered {
			if d.Code == v || (len([]rune(v)) >= 3 && strings.Contains(d.Code, v)) {
				d.Code = ""
			}
			if len([]rune(v)) >= 3 {
				d.Summary = strings.ReplaceAll(d.Summary, v, "[input redacted]")
			} else {
				// Short input values need token boundaries: replacing every 'a' or '1'
				// would destroy all useful context. Quoted short values are scrubbed by
				// SafeDiagnostic independently.
				re := regexp.MustCompile(`(^|[^\pL\pN_])` + regexp.QuoteMeta(v) + `($|[^\pL\pN_])`)
				d.Summary = re.ReplaceAllString(d.Summary, "${1}[input redacted]${2}")
			}
		}
	}
	return SafeDiagnostic(d)
}

// ExplicitDiagnostic is for a locally known protocol failure, never a payload.
func ExplicitDiagnostic(code, format string, a ...any) Diagnostic {
	return SafeDiagnostic(Diagnostic{Code: code, Summary: fmt.Sprintf(format, a...)})
}
