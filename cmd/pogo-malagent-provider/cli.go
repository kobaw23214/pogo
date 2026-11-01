package main

import (
	"bytes"
	"context"
	"os/exec"
)

// cliOutcome is the bounded result of one fixed-argv subprocess invocation.
type cliOutcome struct {
	stdout   []byte
	stderr   []byte
	overflow bool  // stdout or stderr exceeded its capture limit
	timedOut bool  // the caller's context deadline killed the process tree
	err      error // non-nil on start failure or non-zero exit
}

// runCLI is the single subprocess seam: every tool invocation goes through it
// with a fixed argv slice (no shell), a fixed minimal environment and a fixed
// working directory. Tests substitute it with canned outcomes.
var runCLI = func(ctx context.Context, dir string, env []string, argv []string, stdoutLimit int64) cliOutcome {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	stdout := &cappedBuffer{limit: stdoutLimit}
	stderr := &cappedBuffer{limit: 256 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	return cliOutcome{
		stdout:   stdout.buf.Bytes(),
		stderr:   stderr.buf.Bytes(),
		overflow: stdout.overflow || stderr.overflow,
		timedOut: ctx.Err() == context.DeadlineExceeded,
		err:      err,
	}
}

type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int64
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remain := c.limit - int64(c.buf.Len())
	if remain <= 0 {
		c.overflow = true
		return len(p), nil
	}
	if int64(len(p)) > remain {
		_, _ = c.buf.Write(p[:remain])
		c.overflow = true
		return len(p), nil
	}
	return c.buf.Write(p)
}
