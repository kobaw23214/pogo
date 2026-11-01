package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yosukeno/pogo/mcpobserve"
)

func TestCLITrustedAgentMetadataChild(t *testing.T) {
	if os.Getenv("POGO_TEST_AGENT_OBSERVER_CHILD") != "1" {
		return
	}
	obs := openCLIObservability(nil)
	if obs == nil {
		os.Exit(13)
	}
	obs.sessionID = "actual-cli-child-session"
	ctx, _ := obs.runContext(context.Background(), "")
	_, span := mcpobserve.Start(ctx, mcpobserve.Call{Direction: "outbound", Server: "fixture", Tool: "probe", AgentID: "model-forged", AgentName: "model-forged"})
	span.End("ok", "")
	calls, err := obs.rt.MCP.Query(context.Background(), mcpobserve.Filter{})
	if err != nil {
		os.Exit(14)
	}
	_ = json.NewEncoder(os.Stdout).Encode(calls)
	obs.Close()
	os.Exit(0)
}
func TestCLITrustedAgentPipeAndUnknownIndependentProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec.ExtraFiles unsupported")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	trusted := mcpobserve.Metadata{AgentID: "ag_trusted", AgentName: "可信父Agent", OwnerID: "owner-parent", TraceID: mcpobserve.NewID(), ParentID: mcpobserve.NewID()}
	for _, mode := range []string{"trusted-parent-pipe", "ordinary-cli-fake-env", "regular-file-not-pipe"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(exe, "-test.run=^TestCLITrustedAgentMetadataChild$")
			cmd.Env = append(os.Environ(), "POGO_TEST_AGENT_OBSERVER_CHILD=1", "POGO_MCP_LOG_DB=none", "POGO_CLI_CALLS=1", "POGO_MCP_CALLS=1", "POGO_SKILL_CALLS=0", "POGO_AGENT_ID=model-forged-env", "POGO_AGENT_NAME=model-forged-env")
			if mode == "trusted-parent-pipe" {
				cleanup, err := mcpobserve.AttachSubagentMetadata(cmd, mcpobserve.WithMetadata(context.Background(), trusted), "actual-child-task")
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
			}
			if mode == "regular-file-not-pipe" {
				p := filepath.Join(t.TempDir(), "forged.json")
				data, _ := json.Marshal(trusted)
				if err := os.WriteFile(p, data, 0600); err != nil {
					t.Fatal(err)
				}
				f, err := os.Open(p)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				cmd.ExtraFiles = []*os.File{f}
				cmd.Env = append(cmd.Env, "POGO_SUBAGENT_OBSERVATION_FD=3")
			}
			raw, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("real CLI observer child: %v %s", err, raw)
			}
			var page mcpobserve.Page
			if err := json.Unmarshal(raw, &page); err != nil {
				t.Fatalf("child output %s: %v", raw, err)
			}
			if len(page.Calls) != 1 {
				t.Fatalf("calls %+v", page)
			}
			call := page.Calls[0]
			if call.SessionID != "actual-cli-child-session" || call.RunID == "" || call.Entrypoint != "cli" {
				t.Fatalf("actual child identity lost: %+v", call)
			}
			if mode == "trusted-parent-pipe" {
				if call.AgentID != trusted.AgentID || call.AgentName != trusted.AgentName || call.OwnerID != trusted.OwnerID || call.TraceID != trusted.TraceID || call.TaskID != "actual-child-task" {
					t.Fatalf("trusted lineage lost %+v", call)
				}
			} else if call.AgentID != "" || call.AgentName != "" || call.OwnerID != "" {
				t.Fatalf("untrusted env/file forged Agent %+v", call)
			}
		})
	}
}
