package main

import (
	"io"
	"os"

	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/web"
)

// checkpointCommand implements -checkpoints-prune.
//
// Cleanup is an explicit command so an unrelated run never deletes someone's
// restore points. It remains available independently of repository management.
//
// Until this existed the answer was "never": a checkpoint was taken at every run
// start and nothing ever removed one, so a workspace's shadow repo grew for as
// long as the workspace was worked in.
//
// The retention numbers are not flags. A knob here would need a reason to be
// turned, and the honest default — a hundred points or thirty days, whichever
// runs out first — is the same answer for every project. Add the flag when
// someone has a workspace that disproves that.
func checkpointCommand(out io.Writer, cwd string) error {
	// -C defaults to the empty string and every consumer resolves it the same
	// way (see serveWeb, printSkills, printMemory). Resolving it here too is not
	// tidiness: the shadow repo is addressed by a hash of this exact string, so
	// a command that skipped this step would look for the store under
	// sha256("") and truthfully report that a busy workspace has no checkpoints.
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}
	return web.PruneCheckpoints(out, session.DefaultDir(), cwd,
		web.DefaultCheckpointKeep, web.DefaultCheckpointMaxAge)
}
