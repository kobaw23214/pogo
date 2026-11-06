package web

import (
	"errors"
	"path"
	"strings"

	"github.com/yosukeno/pogo/skills"
)

func normalizeStarterContent(entry *starterEntry) error {
	if entry.Parts == nil {
		if entry.InputLayout != "" && entry.InputLayout != "top" && entry.InputLayout != "inline" {
			return errors.New("无效的引用排列方式")
		}
		return nil
	}
	if msg := skills.ValidateStarterParts(entry.Parts, entry.InputLayout); msg != "" {
		return errors.New(msg)
	}
	if _, _, err := normalizeMessageInput("", nil, entry.Parts, entry.InputLayout); err != nil {
		return err
	}
	entry.Prompt = starterInputFallback(entry.Parts)
	return nil
}

// The fallback is readable in old clients and search/export. It never contains
// private absolute paths or the large observation payload behind an element.
func starterInputFallback(parts []MessageInputPart) string {
	var out strings.Builder
	for _, part := range parts {
		switch part.Type {
		case "text":
			out.WriteString(part.Text)
		case "element":
			out.WriteString("【页面元素】")
		case "file":
			out.WriteString("【" + path.Base(part.Path) + "】")
		case "skill":
			out.WriteString("【/skill:" + part.Name + "】")
		case "mcp":
			out.WriteString("【" + skills.MCPInputText(part.Server, part.Name) + "】")
		case "office":
			out.WriteString("【Office选区：" + part.Office.Title + "】")
		case "shortcut":
			out.WriteString("【资料：" + part.Title + "】")
		}
	}
	value := []rune(strings.TrimSpace(out.String()))
	if len(value) > 2000 {
		return string(value[:2000]) + "…"
	}
	return string(value)
}
