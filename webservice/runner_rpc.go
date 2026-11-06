package webservice

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type runnerRPCRequest struct {
	Record Record  `json:"record"`
	Launch *Launch `json:"launch,omitempty"`
	Cursor string  `json:"cursor,omitempty"`
	Limit  int     `json:"limit,omitempty"`
}
type runnerRPCResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// NewRunnerRPCHandler is a private control API, not a model-facing tool/API.
// Deployment must bind it only to the control network and protect its token.
func NewRunnerRPCHandler(runner Runner, token string) http.Handler {
	active := make(chan struct{}, 8)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		respond := func(status int, result any, err error) {
			var response runnerRPCResponse
			if err != nil {
				var typed *Error
				if errors.As(err, &typed) {
					response.Error = typed
				} else {
					response.Error = &Error{Code: "RUNNER_UNAVAILABLE", Message: "runner operation was not confirmed", Retryable: true}
				}
			} else {
				response.Result, _ = json.Marshal(result)
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(response)
		}
		if runner == nil || len(token) < 32 {
			respond(503, nil, Fail("FEATURE_DISABLED", "runner configuration is unavailable"))
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			respond(401, nil, Fail("PERMISSION_DENIED", "runner authentication required"))
			return
		}
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/v1/runner/") {
			respond(404, nil, Fail("NOT_FOUND", "unknown runner operation"))
			return
		}
		action := strings.TrimPrefix(r.URL.Path, "/v1/runner/")
		switch action {
		case "preflight", "profiles", "start", "inspect", "stop", "renew", "logs", "delete-data":
		default:
			respond(404, nil, Fail("NOT_FOUND", "unknown runner operation"))
			return
		}
		select {
		case active <- struct{}{}:
			defer func() { <-active }()
		default:
			respond(429, nil, Fail("RUNNER_CAPACITY", "runner control requests are at capacity"))
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 80<<20))
		decoder.DisallowUnknownFields()
		var input runnerRPCRequest
		if decoder.Decode(&input) != nil {
			respond(400, nil, Fail("INVALID_REQUEST", "invalid runner request"))
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			respond(400, nil, Fail("INVALID_REQUEST", "runner request has trailing data"))
			return
		}
		var result any
		var err error
		switch action {
		case "preflight":
			err = runner.Preflight(r.Context())
		case "profiles":
			if provider, ok := runner.(interface {
				Profiles(context.Context) ([]string, error)
			}); ok {
				result, err = provider.Profiles(r.Context())
			} else {
				result = []string{"python-web"}
			}
		case "start":
			if input.Launch == nil {
				err = Fail("INVALID_REQUEST", "launch is required")
			} else {
				result, err = runner.Start(r.Context(), *input.Launch)
			}
		case "inspect":
			result, err = runner.Inspect(r.Context(), input.Record)
		case "stop":
			err = runner.Stop(r.Context(), input.Record)
		case "renew":
			err = runner.Renew(r.Context(), input.Record)
		case "logs":
			result, err = runner.Logs(r.Context(), input.Record, input.Cursor, input.Limit)
		case "delete-data":
			err = runner.DeleteData(r.Context(), input.Record)
		}
		if err != nil {
			respond(409, nil, err)
			return
		}
		respond(200, result, nil)
	})
}

type RunnerRPCClient struct {
	endpoint, token string
	client          *http.Client
}

func NewRunnerRPCClient(endpoint, token string) (*RunnerRPCClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(token) < 32 {
		return nil, Fail("INVALID_RUNNER_CONFIG", "valid private runner endpoint and token are required")
	}
	return &RunnerRPCClient{endpoint: strings.TrimRight(endpoint, "/"), token: token, client: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *RunnerRPCClient) call(ctx context.Context, action string, input runnerRPCRequest, out any) error {
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/v1/runner/"+action, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.client.Do(req)
	if err != nil {
		return Fail("RUNNER_UNAVAILABLE", "runner connection failed")
	}
	defer res.Body.Close()
	var body runnerRPCResponse
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body) != nil {
		return Fail("RUNNER_UNAVAILABLE", "invalid runner response")
	}
	if body.Error != nil {
		return body.Error
	}
	if res.StatusCode != 200 {
		return Fail("RUNNER_UNAVAILABLE", "runner request rejected")
	}
	if out != nil {
		if err := json.Unmarshal(body.Result, out); err != nil {
			return Fail("RUNNER_UNAVAILABLE", "invalid runner result")
		}
	}
	return nil
}
func (c *RunnerRPCClient) Preflight(ctx context.Context) error {
	return c.call(ctx, "preflight", runnerRPCRequest{}, nil)
}

func (c *RunnerRPCClient) Profiles(ctx context.Context) ([]string, error) {
	var profiles []string
	err := c.call(ctx, "profiles", runnerRPCRequest{}, &profiles)
	return profiles, err
}
func (c *RunnerRPCClient) Start(ctx context.Context, l Launch) (Instance, error) {
	var v Instance
	err := c.call(ctx, "start", runnerRPCRequest{Launch: &l}, &v)
	return v, err
}
func (c *RunnerRPCClient) Inspect(ctx context.Context, r Record) (RuntimeStatus, error) {
	var v RuntimeStatus
	err := c.call(ctx, "inspect", runnerRPCRequest{Record: r}, &v)
	return v, err
}
func (c *RunnerRPCClient) Stop(ctx context.Context, r Record) error {
	return c.call(ctx, "stop", runnerRPCRequest{Record: r}, nil)
}
func (c *RunnerRPCClient) Renew(ctx context.Context, r Record) error {
	return c.call(ctx, "renew", runnerRPCRequest{Record: r}, nil)
}
func (c *RunnerRPCClient) Logs(ctx context.Context, r Record, cursor string, limit int) (LogPage, error) {
	var v LogPage
	err := c.call(ctx, "logs", runnerRPCRequest{Record: r, Cursor: cursor, Limit: limit}, &v)
	return v, err
}
func (c *RunnerRPCClient) DeleteData(ctx context.Context, r Record) error {
	return c.call(ctx, "delete-data", runnerRPCRequest{Record: r}, nil)
}
