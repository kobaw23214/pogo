package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/yosukeno/pogo/tools"
)

// One bounded completion reminder per run, driven by typed live-host receipts.
// It never saves itself: the agent must verify the shared document, respect an
// explicit no-save request and use the normal reviewed docs_office save tool.
// No transcript scan or file contents are retained, and human-only dirty state
// cannot create an entry. Epochs distinguish old idempotent save receipts from
// new changes, and allow a human save to settle the same shared document.
type officeFinishState struct {
	mu        sync.Mutex
	reminded  bool
	documents map[string]officeFinishDocument
}

type officeFinishDocument struct {
	mutation, persisted uint64
	saveAttempted       bool
	blocked             bool
}

func (s *officeFinishState) note(receipt *tools.OfficeCompletionReceipt) {
	if s == nil || receipt == nil || receipt.DocumentSessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.documents[receipt.DocumentSessionID]
	if receipt.MutationEpoch > d.mutation {
		d.mutation = receipt.MutationEpoch
		d.saveAttempted = false
	}
	if d.mutation == 0 {
		return
	}
	if receipt.PersistedEpoch > d.persisted {
		d.persisted = receipt.PersistedEpoch
	}
	d.saveAttempted = d.saveAttempted || receipt.SaveAttempted
	d.blocked = receipt.Blocked
	if s.documents == nil {
		s.documents = make(map[string]officeFinishDocument)
	}
	s.documents[receipt.DocumentSessionID] = d
}

func (s *officeFinishState) notice() (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reminded {
		return "", false
	}
	var pending []string
	for id, d := range s.documents {
		if d.mutation <= d.persisted {
			continue
		}
		status := "尚未保存"
		if d.blocked {
			status = "存在未知操作结果；先检查状态，不得盲目重试或保存"
		} else if d.saveAttempted {
			status = "已尝试保存，但未确认这些修改写入工作区"
		}
		pending = append(pending, fmt.Sprintf("- document_session_id=%s：%s", id, status))
	}
	if len(pending) == 0 {
		return "", false
	}
	s.reminded = true
	sort.Strings(pending)
	// The host bounds sessions; still cap this reminder independently so an
	// unusually long task does not expand the completion prompt without limit.
	if len(pending) > 16 {
		pending = append(pending[:16], fmt.Sprintf("另有 %d 份本轮已修改文档，请用 list 核对。", len(pending)-16))
	}
	return "[pogo] 本轮 Agent 已修改以下 Office 文档，但尚未确认这些修改保存到工作区。" +
		"默认在完成前读取并核对目标及用户的其他输入，然后由你调用 docs_office action:save（新稳定 operation_id），" +
		"收到工作区持久化版本后才能报告已保存。保存整份当前共享文档，保留人工输入，不要用磁盘旧文件覆盖画布。" +
		"若用户明确要求暂不保存、只预览修改，必须遵守，并说明仍未保存；不要为了本提示违背用户要求。" +
		"保存失败或结果未知时先检查状态，只能以同 ID 核对原请求；无法确认则说明未保存/结果未知，不得无限重试。" +
		"不要保存只有人工修改、且不属于本轮 Agent 修改范围的其他文档。无需重复已经完成的修改。\n\n" +
		strings.Join(pending, "\n"), true
}
