package mcpobserve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// This variable names an inherited descriptor, never a JSON identity or model
// flag. Only the controlled Subagent.spawn path creates its bounded pipe. The
// CLI consumes and closes it before any user/model tool can inherit it.
const subagentObservationFD = "POGO_SUBAGENT_OBSERVATION_FD"

// AttachSubagentMetadata carries verified runtime identity across the actual
// child process boundary. ExtraFiles are not supported by Windows; that path
// remains explicitly unattributed instead of trusting an identity environment.
func AttachSubagentMetadata(cmd *exec.Cmd, ctx context.Context, taskID string) (func(), error) {
	env := cmd.Environ()
	clean := env[:0]
	for _, v := range env {
		if !strings.HasPrefix(v, subagentObservationFD+"=") {
			clean = append(clean, v)
		}
	}
	cmd.Env = clean
	md := MetadataFrom(ctx)
	if runtime.GOOS == "windows" || (md.AgentID == "" && md.OwnerID == "" && md.TraceID == "") {
		return func() {}, nil
	}
	c := bounded(Call{AgentID: md.AgentID, AgentName: md.AgentName, OwnerID: md.OwnerID, TraceID: md.TraceID, ParentID: md.ParentID, ParentSpanID: md.ParentSpanID, TraceFlags: md.TraceFlags, ClientID: md.ClientID, TaskID: taskID})
	md = Metadata{AgentID: c.AgentID, AgentName: c.AgentName, OwnerID: c.OwnerID, TraceID: c.TraceID, ParentID: c.ParentID, ParentSpanID: c.ParentSpanID, TraceFlags: c.TraceFlags, ClientID: c.ClientID, TaskID: c.TaskID}
	data, err := json.Marshal(md)
	if err != nil {
		return nil, err
	}
	if len(data) > 8192 {
		return nil, fmt.Errorf("subagent observation metadata exceeds bound")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Env = append(cmd.Env, subagentObservationFD+"="+strconv.Itoa(3+len(cmd.ExtraFiles)))
	cmd.ExtraFiles = append(cmd.ExtraFiles, reader)
	go func() { defer writer.Close(); _, _ = writer.Write(data) }()
	return func() { _ = reader.Close(); _ = writer.Close() }, nil
}

// ReadSubagentMetadata accepts no Agent env/CLI argument. Non-pipe descriptors,
// unbounded/malformed input and ordinary independent CLI processes are unknown.
func ReadSubagentMetadata() Metadata {
	raw := os.Getenv(subagentObservationFD)
	_ = os.Unsetenv(subagentObservationFD)
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 {
		return Metadata{}
	}
	f := observationPipeFile(fd)
	if f == nil {
		return Metadata{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return Metadata{}
	}
	if f.SetReadDeadline(time.Now().Add(2*time.Second)) != nil {
		return Metadata{}
	}
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(data) > 8192 {
		return Metadata{}
	}
	var md Metadata
	if json.Unmarshal(data, &md) != nil {
		return Metadata{}
	}
	// Re-apply the bound at this edge; only the listed parent fields survive.
	c := bounded(Call{AgentID: md.AgentID, AgentName: md.AgentName, OwnerID: md.OwnerID, TraceID: md.TraceID, ParentID: md.ParentID, ParentSpanID: md.ParentSpanID, TraceFlags: md.TraceFlags, ClientID: md.ClientID, TaskID: md.TaskID})
	return Metadata{AgentID: c.AgentID, AgentName: c.AgentName, OwnerID: c.OwnerID, TraceID: c.TraceID, ParentID: c.ParentID, ParentSpanID: c.ParentSpanID, TraceFlags: c.TraceFlags, ClientID: c.ClientID, TaskID: c.TaskID}
}
