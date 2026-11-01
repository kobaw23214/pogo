package web

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/tools"
)

// Register host-known files, not a workspace scan before every command. A
// conversation attachment need not have been opened in Office to be managed.
func (s *Session) registerManagedOfficeAttachment(path string) error {
	extension := strings.ToLower(filepath.Ext(path))
	if extension != ".docx" && extension != ".xlsx" {
		return nil
	}
	if s.mgr == nil || s.mgr.officeRegisterDocument == nil {
		return nil
	}
	absolute, err := tools.Resolve(s.cwd, path)
	if err != nil {
		return err
	}
	info, err := os.Stat(absolute)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return officeFail(400, "PATH_REJECTED", "文档附件必须是当前工作区中的普通文件。")
	}
	return s.mgr.officeRegisterDocument(s.cwd, s.owner, absolute)
}

func (s *Session) seedManagedOfficeAttachments() {
	if s.mgr == nil || s.mgr.officeRegisterDocument == nil {
		return
	}
	references, _ := sessionUploadRefs(s.Path())
	references = append(references, sessionUploadLedger(s.cwd, s.ID)...)
	seen := map[string]bool{}
	for _, path := range references {
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := s.registerManagedOfficeAttachment(path); err != nil {
			s.hub.Publish(Event{Type: EvError, Error: "现有文档的受控修改登记尚未完成；请检查文档状态，勿直接覆盖原文件。"})
		}
	}
}
