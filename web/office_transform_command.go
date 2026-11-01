package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

func officeShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// Only tool-bound links receive a commandSession. HTTP editor operations and
// agents without bash cannot turn a document capability into arbitrary code.
func (l *officeLink) runOfficeTransformCommand(ctx context.Context, script, stagedPath string, timeout time.Duration) (tools.Result, error) {
	s := l.commandSession
	if s == nil || s.gate == nil || !l.commandEnabled || s.ID != l.conversationID || s.cwd != l.cwd || !officeSameOwner(s.owner, l.owner) {
		return tools.Result{}, officeFail(403, "COMMAND_UNAVAILABLE", "当前 Agent 没有命令执行权限。可由有命令权限的 Agent 使用受控副本处理，不能通过 Office 权限绕过。")
	}
	if err := ctx.Err(); err != nil {
		return tools.Result{}, err
	}
	absolute, err := tools.Resolve(l.cwd, stagedPath)
	if err != nil || absolute != stagedPath {
		return tools.Result{}, officeFail(400, "STAGE_PATH_REJECTED", "脚本只能处理本次事务的副本。")
	}
	directory := filepath.Dir(absolute)
	// The staged document is writable by approved commands. Keep executable
	// source in host-private storage so a concurrent command cannot replace it
	// between the review and the read-only sandbox mount.
	privateRoot, err := l.service.batchRoot(l.conversationID)
	if err != nil {
		return tools.Result{}, err
	}
	scriptDirectory, err := os.MkdirTemp(privateRoot, ".transform-script-")
	if err != nil {
		return tools.Result{}, err
	}
	defer os.RemoveAll(scriptDirectory)
	root, err := os.OpenRoot(scriptDirectory)
	if err != nil {
		return tools.Result{}, err
	}
	defer root.Close()
	digest := officeHash([]byte(script))
	name := ".pogo-transform-" + digest + ".py"
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return tools.Result{}, officeFail(409, "SCRIPT_FILE_UNAVAILABLE", "本次脚本文件已存在或副本不可用；先查询原操作，不重复运行。")
	}
	_, err = file.WriteString(script)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return tools.Result{}, err
	}
	defer root.Remove(name)
	scriptPath := filepath.Join(scriptDirectory, name)
	command := "exec python3 -I -B " + officeShellQuote(scriptPath) + " " + officeShellQuote(absolute) +
		"\n# Pogo managed Python source; SHA256=" + digest + "\n# " + strings.ReplaceAll(script, "\n", "\n# ")
	args := struct {
		Command string  `json:"command"`
		Timeout float64 `json:"timeout"`
	}{command, timeout.Seconds()}
	raw, err := json.Marshal(args)
	if err != nil {
		return tools.Result{}, err
	}
	callID := "office_script_" + officeHash([]byte(stagedPath + digest))[:32]
	decision := s.gate.Review(ctx, agent.GateRequest{CallID: callID, ToolName: "bash", Args: raw})
	if !decision.Allow {
		return tools.Result{}, officeFail(403, "COMMAND_DENIED", "文档脚本未获命令执行授权："+decision.Reason)
	}
	if len(decision.Args) > 0 {
		var edited struct {
			Command string  `json:"command"`
			Timeout float64 `json:"timeout"`
		}
		decoder := json.NewDecoder(bytes.NewReader(decision.Args))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&edited) != nil || edited.Command != args.Command || edited.Timeout != args.Timeout {
			return tools.Result{}, officeFail(409, "SCRIPT_REVIEW_CHANGED", "审批后的脚本或命令发生变化，请创建新的文档操作；本次未执行。")
		}
	}
	if l.role == nil || l.role.revoked.Load() {
		return tools.Result{}, officeFail(409, "CONVERSATION_CLOSED", "会话已关闭，脚本未执行。")
	}
	// Staging is the only writable data mount. The shared source document and
	// other workspace files remain readable, including their latest content.
	scope := &tools.CommandScope{Cwd: directory, ReadOnlyRoots: []string{l.cwd, scriptDirectory}}
	result, err := (&tools.Bash{Cwd: directory, Scope: scope, Timeout: timeout, SpillDir: directory}).Execute(ctx, raw)
	if err != nil {
		return result, fmt.Errorf("managed document script: %w", err)
	}
	return result, nil
}
