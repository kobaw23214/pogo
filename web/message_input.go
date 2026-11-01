package web

// MessageInputPart is deliberately smaller than llm.Block: a client may attach
// user data, never manufacture tool calls/results, system messages or file bytes.
import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skills"
)

type MessageInputPart = skills.StarterInputPart

const maxInputParts = 512
const maxInputBytes = 256 << 10

// Native observations escape two independently bounded 2,000-character URLs.
// Entity expansion can exceed 16 KiB without exceeding the producer contract.
const maxInputElementBytes = 32 << 10

var errMessageInputInvalid = errors.New("无效的消息附件")

// normalizeMessageInput retains every text byte and duplicate reference. Only
// the legacy plain prompt is trimmed; inline whitespace is meaningful prose.
func normalizeMessageInput(prompt string, attachments []string, parts []MessageInputPart, layout string) (string, []string, error) {
	if layout != "" && layout != "top" && layout != "inline" {
		return "", nil, errors.New("无效的引用排列方式")
	}
	if parts == nil {
		return strings.TrimSpace(prompt), attachments, nil
	}
	if err := skills.ValidateInputParts(parts, layout, "workspace"); err != "" {
		return "", nil, fmt.Errorf("%w: %s", errMessageInputInvalid, err)
	}
	var text strings.Builder
	var files []string
	firstText := true
	var visit func([]MessageInputPart, bool) error
	visit = func(ps []MessageInputPart, inShortcut bool) error {
		for _, part := range ps {
			switch part.Type {
			case "text":
				// Only top-level typed command atoms invoke host commands. Content inside
				// a frozen material is ordinary data, even if it begins with /skill:.
				if !inShortcut && (slashCommand(strings.TrimSpace(part.Text)) != "" || (!firstText && strings.HasPrefix(strings.TrimSpace(part.Text), "/skill:"))) {
					return errors.New("命令只能通过对应入口执行")
				}
				if strings.TrimSpace(part.Text) != "" {
					firstText = false
				}
				text.WriteString(part.Text)
			case "element":
				text.WriteString(part.Text)
			case "file":
				files = append(files, part.Path)
			case "office":
				text.WriteString(skills.OfficeInputText(part.Office))
			case "shortcut":
				text.WriteString(skills.ShortcutInputText(part))
				if part.Parts != nil {
					if err := visit(part.Parts, true); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := visit(parts, false); err != nil {
		return "", nil, err
	}
	return text.String(), files, nil
}

// Every consumer uses the same preorder traversal for explicit Skill indexes.
func flatInputParts(parts []MessageInputPart) []MessageInputPart { return inputref.Flatten(parts) }

func orderedInputBlocks(parts []MessageInputPart, files map[string][]llm.Block, skillBlocks ...map[int]llm.Block) []llm.Block {
	var blocks []llm.Block
	index := 0
	var visit func([]MessageInputPart)
	visit = func(ps []MessageInputPart) {
		for _, part := range ps {
			current := index
			index++
			switch part.Type {
			case "text", "element":
				if part.Text != "" {
					blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: part.Text, InputElement: part.Type == "element", InputElementFavicon: part.Favicon})
				}
			case "file":
				blocks = append(blocks, files[inputAttachmentKey(part)]...)
			case "skill":
				if len(skillBlocks) > 0 {
					if block, ok := skillBlocks[0][current]; ok {
						blocks = append(blocks, block)
					}
				}
			case "office":
				o := *part.Office
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: skills.OfficeInputText(&o), InputOffice: &o})
			case "shortcut":
				header := len(blocks)
				blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: skills.ShortcutInputText(part), InputShortcut: &llm.InputShortcutSnapshot{ID: part.ShortcutID, Title: part.Title, Text: part.Text, Icon: part.Icon, Language: part.Language, Parts: inputref.Clone(part.Parts)}})
				if part.Parts != nil {
					visit(part.Parts)
					blocks[header].InputShortcut.BlockCount = len(blocks) - header - 1
				}
			}
		}
	}
	visit(parts)
	return blocks
}

func inputAttachmentKey(part MessageInputPart) string {
	if part.AsFile {
		return part.Path + "\x00file"
	}
	return part.Path
}

// prepareInputSkills resolves only explicit atoms. Ordinary prose and observed
// page/file content are never scanned for executable skill instructions.
func (s *Session) prepareInputSkills(parts []MessageInputPart) (map[int]llm.Block, mcpobserve.Metadata, bool, error) {
	var observed mcpobserve.Metadata
	blocks := map[int]llm.Block{}
	available := s.mgr.Skills()
	s.mu.Lock()
	if s.studioID != "" {
		available = append([]skills.Skill(nil), s.invocationSkills...)
	}
	owner := s.owner
	s.mu.Unlock()
	// Resolve every reference before reading any body or granting browser use.
	resolved := map[int]skills.Skill{}
	for index, part := range flatInputParts(parts) {
		if part.Type != "skill" {
			continue
		}
		sk, ok := skills.Find(available, part.Name)
		if !ok {
			return nil, observed, false, fmt.Errorf("%w: no skill named %q", errMessageInputInvalid, part.Name)
		}
		if sk.BrowserTask && !s.mgr.browserAllowed(owner) {
			return nil, observed, false, fmt.Errorf("%w: browser device is not bound", errMessageInputInvalid)
		}
		resolved[index] = sk
	}
	armBrowser := false
	for index, part := range flatInputParts(parts) {
		if part.Type != "skill" {
			continue
		}
		sk := resolved[index]
		expanded, source, err := s.loadSkillExplicit(sk, "")
		if err != nil {
			return nil, observed, false, err
		}
		if observed.TraceID == "" {
			observed = source
		}
		blocks[index] = llm.Block{Type: llm.BlockText, Text: expanded, InputSkill: sk.Name}
		armBrowser = armBrowser || sk.BrowserTask
	}
	return blocks, observed, armBrowser && s.browserToolEnabled(), nil
}

// Check a pending reference before acknowledging it, without doing a Skill
// upload or reading file bytes. OpenRoot refuses escaping symlink targets.
func (s *Session) validateInputFiles(parts []MessageInputPart) error {
	var root *os.Root
	defer func() {
		if root != nil {
			_ = root.Close()
		}
	}()
	for _, part := range flatInputParts(parts) {
		if part.Type != "file" {
			continue
		}
		if s.studioID != "" && !s.studioWeb {
			return errors.New("此 Agent 暂不支持附件")
		}
		if root == nil {
			var err error
			root, err = os.OpenRoot(s.cwd)
			if err != nil {
				return err
			}
		}
		st, err := root.Stat(part.Path)
		if err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("附件 %s 不存在或不是工作区内的普通文件", part.Path)
		}
	}
	return nil
}

// This is a read-only permission check. A typed atom can use only the live
// catalog already selected for this session; it cannot enable another Skill.
func (s *Session) checkInputSkillAccess(parts []MessageInputPart) error {
	available := s.mgr.Skills()
	s.mu.Lock()
	if s.studioID != "" {
		available = append([]skills.Skill(nil), s.invocationSkills...)
	}
	owner := s.owner
	s.mu.Unlock()
	for _, part := range flatInputParts(parts) {
		if part.Type != "skill" {
			continue
		}
		sk, ok := skills.Find(available, part.Name)
		if !ok {
			return fmt.Errorf("%w: no skill named %q", errMessageInputInvalid, part.Name)
		}
		if sk.BrowserTask && !s.mgr.browserAllowed(owner) {
			return fmt.Errorf("%w: browser device is not bound", errMessageInputInvalid)
		}
	}
	return nil
}
