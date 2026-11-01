package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"
)

const officeUnavailableReason = "Office 编辑服务暂时不可用，请稍后重试；聊天和浏览器仍可使用。"
const officeConfigErrorReason = "Office 配置或签名文件无效，请检查部署配置；聊天和浏览器仍可使用。"
const officeReadinessTimeout = 800 * time.Millisecond
const officeReadinessCache = 2 * time.Second

// A health sample is not the user's opening budget. Cold starts and transient
// upstream failures are retried within this independent five-minute stage.
const officeOpenWaitBudget = 5 * time.Minute
const officeOpenHTTPBudget = officeOpenWaitBudget + 10*time.Second
const officeOpenProbeBudget = 5 * time.Second
const officeOpenProbeRetry = 2 * time.Second

var officeHealthSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)

type officeAvailability struct {
	Enabled    bool   `json:"enabled"` // Valid configuration, not upstream readiness.
	Configured bool   `json:"configured"`
	State      string `json:"state"`
	Ready      bool   `json:"ready"`
	Reason     string `json:"reason"`
	BrowserURL string `json:"browserURL"`
}

// Called on demand, never during web startup. Probes read only the fixed
// internal health endpoint; concurrent callers share a bounded probe.
func (o *officeService) editorReady(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, officeReadinessTimeout)
	defer cancel()
	for {
		if ctx.Err() != nil {
			return false
		}
		o.readinessMu.Lock()
		if time.Now().Before(o.readinessUntil) {
			ready := o.readinessReady
			o.readinessMu.Unlock()
			return ready
		}
		if pending := o.readinessPending; pending != nil {
			o.readinessMu.Unlock()
			select {
			case <-ctx.Done():
				return false
			case <-pending:
				continue
			}
		}
		pending := make(chan struct{})
		o.readinessPending = pending
		o.readinessMu.Unlock()
		ready := o.probeEditor(ctx)
		o.readinessMu.Lock()
		// A caller cancellation is not evidence about the shared upstream.
		if parent.Err() == nil {
			o.readinessReady, o.readinessUntil = ready, time.Now().Add(officeReadinessCache)
		}
		o.readinessPending = nil
		close(pending)
		o.readinessMu.Unlock()
		return ready
	}
}

// Unlike the lightweight availability endpoint, an actual open waits for the
// configured editor to recover. Concurrent opens share each health request;
// negative cached samples cannot reject a new open or shorten its budget.
func (o *officeService) waitEditorReady(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, officeOpenWaitBudget)
	defer cancel()
	for {
		if err := parent.Err(); err != nil {
			if err == context.DeadlineExceeded {
				return officeFail(http.StatusGatewayTimeout, "OFFICE_OPEN_TIMEOUT", "Office 打开请求已超时，请重试；现有文档内容与会话已保留。")
			}
			return err
		}
		if ctx.Err() != nil {
			return officeFail(http.StatusGatewayTimeout, "OFFICE_OPEN_TIMEOUT", "Office 编辑服务在 5 分钟内未就绪，请重试；现有文档内容与会话已保留。")
		}
		o.readinessMu.Lock()
		if o.readinessReady && time.Now().Before(o.readinessUntil) {
			o.readinessMu.Unlock()
			return nil
		}
		if delay := time.Until(o.openReadinessNext); delay > 0 {
			o.readinessMu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
			timer.Stop()
			continue
		}
		if pending := o.openReadinessPending; pending != nil {
			o.readinessMu.Unlock()
			select {
			case <-ctx.Done():
				continue
			case <-pending:
				continue
			}
		}
		pending := make(chan struct{})
		o.openReadinessPending = pending
		o.readinessMu.Unlock()
		probeContext, finishProbe := context.WithTimeout(ctx, officeOpenProbeBudget)
		ready, probeErr := o.probeEditorResult(probeContext)
		finishProbe()
		o.readinessMu.Lock()
		if ready && ctx.Err() == nil {
			o.readinessReady, o.readinessUntil = true, time.Now().Add(officeReadinessCache)
			o.openReadinessNext = time.Time{}
		} else if ctx.Err() == nil {
			o.openReadinessNext = time.Now().Add(officeOpenProbeRetry)
		}
		o.openReadinessPending = nil
		close(pending)
		o.readinessMu.Unlock()
		if ready && ctx.Err() == nil {
			return nil
		}
		var permanent *officeError
		if ctx.Err() == nil && errors.As(probeErr, &permanent) && permanent.code == "OFFICE_CONFIG_INVALID" {
			return permanent
		}
	}
}

func (o *officeService) probeEditor(ctx context.Context) bool {
	ready, _ := o.probeEditorResult(ctx)
	return ready
}

func (o *officeService) probeEditorResult(ctx context.Context) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, o.cfg.serviceURL+"/pogo-office/healthz", nil)
	if err != nil {
		return false, err
	}
	client := *o.cfg.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusRequestTimeout && response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusTooEarly {
			return false, officeFail(http.StatusServiceUnavailable, "OFFICE_CONFIG_INVALID", "Office 编辑服务拒绝就绪检查，请检查内部服务地址、认证和版本配置；现有文档内容已保留。")
		}
		return false, nil
	}
	var health struct {
		Ready           bool   `json:"ready"`
		Adapter         string `json:"adapter"`
		EngineVersion   string `json:"engineVersion"`
		VerifiedAssets  int    `json:"verifiedAssets"`
		ExtensionSHA256 string `json:"extensionSha256"`
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return false, err
	}
	if len(body) > 4096 || json.Unmarshal(body, &health) != nil {
		return false, officeFail(http.StatusServiceUnavailable, "OFFICE_CONFIG_INVALID", "Office 编辑服务返回了无效的就绪信息，请检查内部服务配置。")
	}
	if health.Adapter != "pogo-office-history" || health.EngineVersion != "9.4.0 (build:129)" || health.VerifiedAssets != 9 || !officeHealthSHA.MatchString(health.ExtensionSHA256) {
		return false, officeFail(http.StatusServiceUnavailable, "OFFICE_CONFIG_INVALID", "Office 编辑服务的版本或扩展身份不匹配，请检查部署配置。")
	}
	return health.Ready, nil
}

func (s *Server) officeAvailability(ctx context.Context) officeAvailability {
	if s.office == nil {
		if s.officeConfigError {
			return officeAvailability{Configured: true, State: "config_error", Reason: officeConfigErrorReason}
		}
		return officeAvailability{State: "disabled", Reason: "当前部署尚未配置 Office 编辑服务。"}
	}
	status := officeAvailability{Enabled: true, Configured: true, State: "unavailable", Reason: officeUnavailableReason, BrowserURL: officeGatewayPrefix}
	if s.office.editorReady(ctx) {
		status.Ready, status.State, status.Reason = true, "ready", ""
	}
	return status
}
