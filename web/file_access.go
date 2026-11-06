package web

import (
	"fmt"

	"github.com/yosukeno/pogo/tools"
)

// File access follows the same live, session-local policy as approval. Keeping
// the callback on tool instances also covers limited auto expiry and tools
// retained across native/Skill configuration refreshes.
func (s *Session) fileAccess() *tools.FileAccess {
	return &tools.FileAccess{Root: s.root, FullAccess: s.fullFileAccess}
}

func (s *Session) fullFileAccess() bool {
	return s != nil && s.policy != nil && s.policy.State().Mode == string(ModeAuto)
}

func (s *Session) fileAccessPrompt() string {
	if s.fullFileAccess() {
		return fmt.Sprintf("File access: full access is enabled for this conversation. Ordinary project file tools may read and modify files throughout your account workspace %q, including sibling Agent projects. Relative paths still start from the current working directory %q. Account identity, host-private control storage and managed-document editing protections remain enforced.", s.root, s.cwd)
	}
	return fmt.Sprintf("File access: this conversation's project file tools are limited to the working directory %q and their explicit readable/writable roots. Parent directories and sibling Agent projects are not automatically granted. A switch to full access applies to this conversation's account workspace, not other accounts.", s.cwd)
}
