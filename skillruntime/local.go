package skillruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/yosukeno/pogo/skillruntime/core"
)

const LocalProfile = "sandbox:local-bwrap-v1"

// LocalExecutor launches one private namespace/process tree inside Pogo's
// existing container. It never connects to Docker or falls back to a raw exec.
type LocalExecutor struct {
	root, workDir, bwrap, helper, identity string
	memoryBudgetMiB                        int
	capacity                               chan struct{}
	platform                               platformSpec
}

func (e *LocalExecutor) Identity() string          { return e.identity }
func (e *LocalExecutor) ExecutionPlatform() string { return e.platform.name }
func (e *LocalExecutor) Execute(ctx context.Context, in Execution) ([]byte, error) {
	out, err := e.ExecuteDetailed(ctx, in)
	return out.Output, err
}

// The profile records the enforcement mechanism as part of the host grant.
// CPU/RSS/process counts use supervision, not per-call cgroup hard limits.
type ExecutionStatus struct {
	Configured         bool     `json:"configured"`
	Backend            string   `json:"backend"`
	Label              string   `json:"label"`
	ResourceLimits     string   `json:"resource_limits"`
	HostPlatform       string   `json:"host_platform"`
	ExpectedPlatform   string   `json:"expected_platform,omitempty"`
	SupportedPlatforms []string `json:"supported_platforms"`
	IssueCode          string   `json:"issue_code,omitempty"`
	Issue              string   `json:"issue,omitempty"`
}

func (s *Service) ExecutionStatus() ExecutionStatus {
	p := s.platformState()
	out := ExecutionStatus{Configured: s.Executor != nil, Backend: "local", Label: "同容器进程沙箱", ResourceLimits: "调用超时与输出大小限制；内存、CPU 和进程数量由监控控制，硬资源上限由整个 Pogo 容器共享。", HostPlatform: p.host, ExpectedPlatform: p.expected, SupportedPlatforms: supportedLocalPlatforms()}
	if out.Configured {
		return out
	}
	switch {
	case s.ExecutionIssue != "":
		out.Issue, out.IssueCode = s.ExecutionIssue, s.ExecutionIssueCode
		if out.IssueCode == "" {
			out.IssueCode = "EXECUTOR_UNAVAILABLE"
		}
	case p.issue != nil:
		out.Issue, out.IssueCode = p.issue.Message, p.issue.Code
	default:
		out.IssueCode, out.Issue = "EXECUTOR_REQUIRED", "Skill 执行环境尚未配置；可先安装 Skill 和保存草稿"
	}
	return out
}

func (s *Service) selectionProfile(l core.Limits) (string, error) {
	if e, ok := s.Executor.(*LocalExecutor); ok && e.memoryBudgetMiB > 0 && l.MemoryMiB > e.memoryBudgetMiB {
		return "", fmt.Errorf("Skill 声明需要 %d MiB 内存，当前容器为分析预留的预算为 %d MiB；请提高 POGO_MEMORY_LIMIT 后重启", l.MemoryMiB, e.memoryBudgetMiB)
	}
	return LocalProfile, hostCeiling(LocalProfile, l)
}

func (s *Service) checkExecutionProfile(profile string) error {
	if profile != LocalProfile {
		return errors.New("UNSUPPORTED_ISOLATION_PROFILE: 仅支持同容器进程沙箱")
	}
	return nil
}

type localResult struct {
	Protocol string         `json:"protocol"`
	Result   DetailedResult `json:"result"`
	Error    string         `json:"error,omitempty"`
}

const localProtocol = "pogo.local-executor/1"

func structLimits(l ExecutorLimits) core.Limits {
	return core.Limits{TimeoutSeconds: l.TimeoutSeconds, MemoryMiB: l.MemoryMiB, CPUMilliCores: l.CPUMilliCores, MaxOutputBytes: l.MaxOutputBytes}
}
