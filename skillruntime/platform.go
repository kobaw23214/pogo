package skillruntime

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yosukeno/pogo/skillruntime/core"
)

const EnvPlatform = "POGO_PLATFORM"

// ExecutionError keeps diagnostics machine-readable without parsing translated
// messages. It describes a blocked execution, not a failed package installation.
type ExecutionError struct {
	Code    string
	Message string
}

func (e *ExecutionError) Error() string { return e.Code + ": " + e.Message }

func executionError(code, format string, args ...any) *ExecutionError {
	return &ExecutionError{Code: code, Message: fmt.Sprintf(format, args...)}
}

type platformSpec struct {
	name, triplet string
	machine       elf.Machine
}

func localPlatformSpec(name string) (platformSpec, bool) {
	switch name {
	case "linux/amd64":
		return platformSpec{name, "x86_64-linux-gnu", elf.EM_X86_64}, true
	case "linux/arm64":
		return platformSpec{name, "aarch64-linux-gnu", elf.EM_AARCH64}, true
	default:
		return platformSpec{}, false
	}
}

func supportedLocalPlatforms() []string { return []string{"linux/amd64", "linux/arm64"} }

type platformState struct {
	host, expected string
	spec           platformSpec
	issue          *ExecutionError
}

// The environment is an assertion, never a substitute for the running Go
// binary's platform. A matching image under emulation reports its own GOARCH.
func detectPlatform(goos, goarch, expected string) platformState {
	p := platformState{host: goos + "/" + goarch, expected: strings.TrimSpace(expected)}
	if p.expected != "" {
		if _, ok := localPlatformSpec(p.expected); !ok {
			p.issue = executionError("INVALID_PLATFORM", "%s=%q 无效；请使用 linux/amd64、linux/arm64，或留空自动检测", EnvPlatform, p.expected)
			return p
		}
		if p.expected != p.host {
			p.issue = executionError("PLATFORM_MISMATCH", "%s 声明 %s，但当前 Pogo 运行于 %s；请使用匹配的镜像或修正平台声明，不能用环境变量转换架构", EnvPlatform, p.expected, p.host)
			return p
		}
	}
	var ok bool
	p.spec, ok = localPlatformSpec(p.host)
	if !ok {
		p.issue = executionError("UNSUPPORTED_HOST_PLATFORM", "当前 Pogo 运行于 %s；上传式 Skill 执行需要 Linux/amd64 或 Linux/arm64 与 bubblewrap，请使用匹配的 Pogo 容器", p.host)
	}
	return p
}

func currentPlatform() platformState {
	return detectPlatform(runtime.GOOS, runtime.GOARCH, os.Getenv(EnvPlatform))
}

func (p platformSpec) systemLibraries() []string {
	return []string{
		filepath.Join("/lib", p.triplet, "libc.so.6"),
		filepath.Join("/lib", p.triplet, "libgcc_s.so.1"),
		filepath.Join("/usr/lib", p.triplet, "libstdc++.so.6"),
		filepath.Join("/lib", p.triplet, "libz.so.1"),
	}
}

func (p platformSpec) validateELFHeader(header []byte) error {
	if len(header) < 20 || !bytes.Equal(header[:4], []byte{0x7f, 'E', 'L', 'F'}) ||
		header[4] != byte(elf.ELFCLASS64) || header[5] != byte(elf.ELFDATA2LSB) {
		return executionError("INVALID_MANIFEST", "provider 必须是 %s 的 64 位小端 ELF 可执行文件，不能是安装脚本", p.name)
	}
	actual := elf.Machine(binary.LittleEndian.Uint16(header[18:20]))
	if actual != p.machine {
		return executionError("UNSUPPORTED_PLATFORM", "provider ELF 架构 %s 与当前执行平台 %s 不匹配；请安装对应架构的 Skill 包", actual, p.name)
	}
	return nil
}

func (s *Service) platformState() platformState {
	if s != nil && s.platform != nil {
		return *s.platform
	}
	return currentPlatform()
}

// A trusted executor may describe its execution target (for example a test
// executor). LocalExecutor always returns the actual platform detected at init.
// Neither a Skill manifest nor request input can select this target.
func (s *Service) executionPlatform() string {
	if p, ok := s.Executor.(interface{ ExecutionPlatform() string }); ok {
		return p.ExecutionPlatform()
	}
	return s.platformState().host
}

func runtimePlatformIssue(p core.Platform, target string) *ExecutionError {
	name := p.OS + "/" + p.Arch
	if _, ok := localPlatformSpec(name); !ok || p.Libc != "glibc" || p.MinLibcVersion != "2.36" {
		return executionError("UNSUPPORTED_PLATFORM", "Skill 声明 %s、%s %s；当前支持 Linux/amd64 或 Linux/arm64 的 glibc 2.36 运行配置", name, p.Libc, p.MinLibcVersion)
	}
	if name != target {
		return executionError("UNSUPPORTED_PLATFORM", "Skill 需要 %s，当前执行平台为 %s；请安装 %s 版本的 Skill 包，或在匹配的部署中运行", name, target, target)
	}
	return nil
}

func (s *Service) packagePlatformIssues(p *core.Package) []SelectionIssue {
	used := make(map[string]bool)
	for _, provider := range p.Manifest.Providers {
		if provider.Driver == core.DriverProcessJSON {
			used[provider.RuntimeID] = true
		}
	}
	var out []SelectionIssue
	for _, rt := range p.Manifest.Runtimes {
		if !used[rt.ID] {
			continue
		}
		if issue := runtimePlatformIssue(rt.Platform, s.executionPlatform()); issue != nil {
			out = append(out, SelectionIssue{Code: issue.Code, Message: p.Manifest.DisplayName + "：" + issue.Message})
		}
	}
	return out
}

// SetExecutionError records startup diagnostics while keeping package management
// usable. Call only during service initialization, before publishing the service.
func (s *Service) SetExecutionError(err error) {
	s.ExecutionIssue, s.ExecutionIssueCode = "", ""
	if err == nil {
		return
	}
	var issue *ExecutionError
	if errors.As(err, &issue) {
		s.ExecutionIssue, s.ExecutionIssueCode = issue.Message, issue.Code
		return
	}
	s.ExecutionIssue, s.ExecutionIssueCode = err.Error(), "EXECUTOR_UNAVAILABLE"
}
