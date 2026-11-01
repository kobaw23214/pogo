//go:build !unix

package mcpobserve

import "os"

// exec.ExtraFiles has no supported inherited-pipe contract on this platform.
// Unknown is safer than accepting an environment identity as authoritative.
func observationPipeFile(int) *os.File { return nil }
