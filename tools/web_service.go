package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/yosukeno/pogo/webservice"
)

const WebServiceName = "web_service"

// WebService only contains a host-bound backend: callers cannot choose an
// owner, conversation, Docker image, network, credentials or upstream URL.
type WebService struct{ Backend webservice.Backend }

func (*WebService) Name() string { return WebServiceName }
func (*WebService) Description() string {
	return "Start and manage an isolated website using one of the configured Python or Node.js profiles. Only standard-library applications are supported; dependency installation and build steps are unavailable. Applications accept incoming HTTP but cannot initiate TCP, UDP or Unix connections. Static HTML and local assets use HTML Preview instead. Set a stable internal name and an optional human-readable display_name. Omit lease_seconds to use the platform default; the website stops at its deadline. Reading status/logs/open does not renew it. Rename does not restart the website. start returns acceptance, not readiness: inspect status, then open when ready. The returned website address stays valid for its service lease, including renewals and restarts. Source changes need restart. Use only returned service IDs and URLs; never guess localhost or change Docker ports. service_url opens the authenticated owner service card; preview_url is for the current client's iframe preview; the native browser tool must use only a non-null browser_url. If browser_url is null, do not fall back to preview_url or service_url for native browser navigation, rewrite their host or port, or guess an internal address. Keep the client iframe preview available and report that the native route is unverified. After the platform fixes and verifies the route, open again to obtain a non-null browser_url before native browser validation; existing browser availability and task authorization still apply."
}
func (*WebService) ExecutionMode() ExecutionMode { return Sequential }
func (*WebService) ExecutionModeForArgs(raw json.RawMessage) ExecutionMode {
	if WebServiceReadOnly(raw) {
		return Parallel
	}
	return Sequential
}
func (t *WebService) InputSchema() map[string]any {
	profiles := []string{"python-web"}
	if source, ok := t.Backend.(interface{ Profiles() []string }); ok {
		profiles = source.Profiles()
	}
	spec := map[string]any{
		"name":          prop("string", "start only: stable lowercase slot, 1-48 letters/digits/hyphens. Reuse an existing slot via its returned service_id; use rename for its display name"),
		"display_name":  prop("string", "start or rename: human-readable website name, 1-80 characters; supports Chinese"),
		"project_root":  prop("string", "start, or inside restart.replacement_spec: project directory relative to this conversation's workspace"),
		"profile":       map[string]any{"type": "string", "enum": profiles, "description": "start, or inside restart.replacement_spec: an available standard-library runtime"},
		"start_argv":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 8192}, "minItems": 1, "maxItems": 64, "description": "start, or inside restart.replacement_spec: executable and arguments; complete tokens {{HOST}} and {{PORT}} are substituted by the platform"},
		"build_argv":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 0, "description": "Build steps are unavailable in these profiles; omit this field"},
		"health_path":   prop("string", "start, or inside restart.replacement_spec: local HTTP health path, default /; must return HTTP 2xx. It is not a general HTTP test tool"),
		"lease_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": webservice.MaxLeaseSeconds, "description": "Service lifetime in seconds; omitted on start uses the platform default. Renewal is explicit."},
	}
	p := map[string]any{
		"action":       map[string]any{"type": "string", "enum": []string{"start", "list", "status", "logs", "open", "stop", "restart", "renew", "rename"}, "description": "start: top-level startup fields for a new lease; restart: existing service_id, optionally a complete replacement_spec; renew: active service_id + lease_seconds; rename: service_id + display_name. list/status/logs observe, open returns the address, stop ends execution"},
		"operation_id": prop("string", "Unique request ID for mutating actions; reuse unchanged after a lost response"),
		"service_id":   prop("string", "Exact service ID returned by this tool"),
		"generation":   prop("integer", "Optional historical generation for logs"),
		"cursor":       prop("string", "Opaque pagination cursor returned by this tool"),
		"limit_bytes":  map[string]any{"type": "integer", "minimum": 1, "maximum": 65536},
	}
	for k, v := range spec {
		p[k] = v
	}
	replacement := map[string]any{}
	for k, v := range spec {
		if k != "lease_seconds" && k != "name" && k != "display_name" {
			replacement[k] = v
		}
	}
	p["replacement_spec"] = object([]string{"project_root", "profile", "start_argv"}, replacement)
	p["replacement_spec"].(map[string]any)["description"] = "restart only: complete replacement startup settings, not a partial patch. Include project_root, profile and start_argv; retain unchanged values from status.spec. Source-only restart omits this object. name and lease are changed separately with rename/renew"
	schema := object([]string{"action"}, p)
	schema["additionalProperties"] = false
	p["replacement_spec"].(map[string]any)["additionalProperties"] = false
	return schema
}

var webServiceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var webServiceSlot = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

func decodeWebService(raw json.RawMessage) (webservice.Request, error) {
	var req webservice.Request
	if len(raw) > 64<<10 {
		return req, errors.New("web_service arguments exceed 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		return req, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return req, errors.New("expected one JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return req, errors.New("expected a JSON object")
	}
	allowed := map[string]bool{"action": true}
	switch req.Action {
	case "start":
		for _, name := range []string{"operation_id", "name", "display_name", "project_root", "profile", "start_argv", "build_argv", "health_path", "lease_seconds"} {
			allowed[name] = true
		}
		if !webServiceSlot.MatchString(req.Name) || strings.TrimSpace(req.ProjectRoot) == "" || (req.Profile != "python-web" && req.Profile != "node-web") || len(req.StartArgv) == 0 {
			return req, errors.New("start requires a valid name, project_root, supported profile and start_argv")
		}
	case "list":
		allowed["cursor"] = true
	case "status":
		allowed["service_id"] = true
	case "logs":
		for _, name := range []string{"service_id", "generation", "cursor", "limit_bytes"} {
			allowed[name] = true
		}
	case "open", "stop":
		allowed["service_id"], allowed["operation_id"] = true, true
	case "restart":
		allowed["service_id"], allowed["operation_id"], allowed["replacement_spec"] = true, true, true
		if rawReplacement, ok := fields["replacement_spec"]; ok {
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(rawReplacement, &nested); err != nil || nested == nil {
				return req, errors.New("replacement_spec must be an object")
			}
			for key := range nested {
				if key != "project_root" && key != "profile" && key != "start_argv" && key != "build_argv" && key != "health_path" {
					return req, fmt.Errorf("replacement_spec does not accept %s", key)
				}
			}
		}
		if req.Replacement != nil && (req.Replacement.ProjectRoot == "" || (req.Replacement.Profile != "python-web" && req.Replacement.Profile != "node-web") || len(req.Replacement.StartArgv) == 0 || req.Replacement.Name != "" || req.Replacement.LeaseSeconds != 0) {
			return req, errors.New("replacement_spec requires project_root, supported profile and start_argv; name/lease are not replaceable")
		}
	case "renew":
		allowed["service_id"], allowed["operation_id"], allowed["lease_seconds"] = true, true, true
		if req.LeaseSeconds < 1 {
			return req, errors.New("renew requires lease_seconds")
		}
	case "rename":
		allowed["service_id"], allowed["operation_id"], allowed["display_name"] = true, true, true
		if strings.TrimSpace(req.DisplayName) == "" {
			return req, errors.New("rename requires display_name")
		}
	default:
		return req, errors.New("unknown web_service action")
	}
	for k := range fields {
		if !allowed[k] {
			return req, webServiceFieldError(req.Action, k)
		}
	}
	if len(req.BuildArgv) > 0 || req.Replacement != nil && len(req.Replacement.BuildArgv) > 0 {
		return req, errors.New("build_argv is unavailable: these profiles run standard-library applications without installation or build steps; omit build_argv")
	}
	if req.Action != "start" && req.Action != "list" && !webServiceID.MatchString(req.ServiceID) {
		return req, errors.New("service_id must be an ID returned by this tool")
	}
	if req.Action != "list" && req.Action != "status" && req.Action != "logs" && !webServiceID.MatchString(req.OperationID) {
		return req, errors.New("operation_id must be 1-128 letters, digits, hyphens or underscores")
	}
	if req.LeaseSeconds < 0 || req.LeaseSeconds > webservice.MaxLeaseSeconds || req.LimitBytes < 0 || req.LimitBytes > 65536 || req.Generation < 0 {
		return req, errors.New("web_service numeric limit is out of range")
	}
	for _, argv := range [][]string{req.StartArgv, req.BuildArgv} {
		if len(argv) > 64 {
			return req, errors.New("argv contains more than 64 tokens")
		}
		for _, arg := range argv {
			if arg == "" || len(arg) > 8192 || strings.ContainsRune(arg, 0) {
				return req, errors.New("argv entries must be nonempty strings without NUL")
			}
		}
	}
	return req, nil
}

func webServiceFieldError(action, field string) error {
	message := fmt.Sprintf("%s does not accept %s", action, field)
	switch {
	case action == "restart" && (field == "health_path" || field == "project_root" || field == "profile" || field == "start_argv" || field == "build_argv"):
		message += "; put startup changes inside replacement_spec with project_root, profile and start_argv. Copy unchanged settings from status.spec; source-only restart needs service_id and operation_id only"
	case action == "start" && field == "replacement_spec":
		message += "; new starts take project_root, profile, start_argv and optional health_path at top level. Existing websites use restart with their returned service_id and a complete replacement_spec"
	case action == "restart" && field == "lease_seconds":
		message += "; restart preserves the current deadline. Use renew with lease_seconds before the website expires"
	}
	return errors.New(message)
}
func (*WebService) ValidateArgs(raw json.RawMessage) error {
	_, err := decodeWebService(raw)
	return err
}
func WebServiceReadOnly(raw json.RawMessage) bool {
	req, err := decodeWebService(raw)
	return err == nil && (req.Action == "list" || req.Action == "status" || req.Action == "logs")
}
func (t *WebService) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	req, err := decodeWebService(raw)
	if err != nil {
		return Result{}, err
	}
	if t.Backend == nil {
		return Result{}, webservice.Fail("FEATURE_DISABLED", "managed Web services are not configured")
	}
	data, err := t.Backend.Execute(ctx, req)
	if err != nil {
		var execution *webservice.Error
		if errors.As(err, &execution) {
			text := err.Error()
			switch execution.Code {
			case "SERVICE_ALREADY_EXISTS":
				text += "\nRecovery: use list/status to obtain this website's service_id. Apply saved source with restart; change startup settings with restart.replacement_spec including project_root, profile and start_argv. Keep the existing website identity."
			case "LEASE_EXPIRED":
				text += "\nRecovery: read status.spec, then start the same slot with its name, project_root, profile and start_argv to begin a new lease. Retained /data is preserved. An expired service cannot be renewed or restarted."
			}
			return Result{Text: text, Details: map[string]any{"error": execution}}, err
		}
		return Result{}, err
	}
	var details any
	if err := json.Unmarshal(data, &details); err != nil {
		return Result{}, errors.New("invalid managed service response")
	}
	return Result{Text: string(data), Details: details}, nil
}
