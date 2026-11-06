package web

import (
	"bytes"
	"encoding/json"

	"github.com/yosukeno/pogo/tools"
)

// projectedToolResultError repairs the reading view of old native Browser
// results whose executor failure was historically recorded as a successful
// tool call. It never changes the stored block or infers status from page text.
// Portable JSON exports and StoredResult original status remain untouched.
func projectedToolResultError(name string, original bool, raw json.RawMessage) bool {
	if original {
		return true
	}
	if name != "browser" || len(raw) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || !bytes.Equal(bytes.TrimSpace(fields["ok"]), []byte("false")) {
		return false
	}
	if value, present := fields["backend"]; present {
		var backend string
		if json.Unmarshal(value, &backend) != nil || backend != tools.BrowserBackendNative {
			return false
		}
	}
	var action string
	if json.Unmarshal(fields["action"], &action) != nil {
		return false
	}
	switch action {
	case tools.BrowserActionSnapshot, tools.BrowserActionClick, tools.BrowserActionType,
		tools.BrowserActionNavigate, tools.BrowserActionWait, tools.BrowserActionExtract,
		tools.BrowserActionScroll, tools.BrowserActionFind, tools.BrowserActionSelect,
		tools.BrowserActionPress, tools.BrowserActionHover, tools.BrowserActionBatch,
		tools.BrowserActionScreenshot, tools.BrowserActionTabs, tools.BrowserActionBookmarks,
		tools.BrowserActionDownloads, tools.BrowserActionUpload, tools.BrowserActionTab,
		tools.BrowserActionCalculate, tools.BrowserActionDevTools, tools.BrowserActionOpenPages,
		tools.BrowserActionObservePages:
		return true
	default:
		return false
	}
}
