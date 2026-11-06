package skills

import (
	"encoding/base64"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/yosukeno/pogo/inputref"
)

var starterElementShape = regexp.MustCompile(`(?s)^<page-element source="[^"\r\n]*" url="[^"\r\n]*">\n.+\n</page-element>$`)
var starterAssetShape = regexp.MustCompile(`^starter-assets/[a-f0-9]{32}/[^/]+$`)
var informationOriginID = regexp.MustCompile(`^inf_[a-f0-9]{32}$`)
var informationDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var informationDiskVersion = regexp.MustCompile(`^disk:[a-f0-9]{64}$`)
var informationAssetShape = regexp.MustCompile(`^information-assets/[a-f0-9]{64}/[^/]+$`)

// ValidateStarterParts validates saved input without granting access to bytes.
// HTTP materialization separately checks the owner/Agent asset namespace.
func ValidateStarterParts(parts []StarterInputPart, layout string) string {
	return ValidateInputParts(parts, layout, "starter")
}

// ValidateInputParts validates a complete tree before any filesystem or Skill
// action. The byte budget includes frozen display metadata and fallback text.
// fileMode: workspace permits safe relative paths; starter/shortcut require
// durable opaque assets; information requires owned content-addressed assets.
// All nodes share one budget (never per subtree).
func ValidateInputParts(parts []StarterInputPart, layout, fileMode string) string {
	if layout != "" && layout != "top" && layout != "inline" {
		return "invalid input layout"
	}
	if len(parts) == 0 {
		return "invalid ordered input count"
	}
	nodes, size, meaningful := 0, 0, false
	ancestors := map[string]bool{}
	var visit func([]StarterInputPart, int) string
	visit = func(ps []StarterInputPart, depth int) string {
		for _, part := range ps {
			nodes++
			if nodes > 512 {
				return "ordered input exceeds 512 nodes"
			}
			size += len(part.Type) + len(part.Text) + len(part.Path) + len(part.Name) + len(part.Server) + len(part.Favicon) + len(part.ShortcutID) + len(part.Title) + len(part.Icon) + len(part.Language)
			if part.Office != nil {
				o := part.Office
				size += len(o.Path) + len(o.Title) + len(o.Editor) + len(o.Text) + len(o.Sheet) + len(o.Range) + len(o.CanvasVersion) + len(o.CapturedAt) + len(o.SourceSession)
				if o.Locator != nil {
					size += len(o.Locator.Kind) + len(o.Locator.Exact) + len(o.Locator.Prefix) + len(o.Locator.Suffix)
				}
				if o.Information != nil {
					size += len(o.Information.EntryID) + len(o.Information.SnapshotSHA256) + 16
				}
			}
			if size > 256<<10 {
				return "ordered input exceeds 256 KiB"
			}
			if part.Type != "shortcut" && (part.ShortcutID != "" || part.Title != "" || part.Icon != "" || part.Language != "" || part.Parts != nil) {
				return "shortcut metadata requires a shortcut snapshot"
			}
			if part.Type != "office" && part.Office != nil {
				return "office metadata requires an Office snapshot"
			}
			if part.Type != "mcp" && part.Server != "" {
				return "server metadata requires an MCP reference"
			}
			switch part.Type {
			case "text":
				if part.Path != "" || part.AsFile || part.Name != "" || part.Favicon != "" || !utf8.ValidString(part.Text) {
					return "invalid text content"
				}
				meaningful = meaningful || strings.TrimSpace(part.Text) != ""
			case "element":
				if part.Path != "" || part.AsFile || part.Name != "" || len(part.Text) > 32<<10 || !starterElementShape.MatchString(part.Text) || strings.Count(part.Text, "<page-element ") != 1 || strings.Count(part.Text, "</page-element>") != 1 || !ValidInputElementFavicon(part.Favicon) {
					return "invalid page element reference"
				}
				meaningful = true
			case "file":
				if part.Text != "" || part.Name != "" || part.Favicon != "" || !ValidInputRelativePath(part.Path) {
					return "invalid attachment path"
				}
				if fileMode == "starter" && !starterAssetShape.MatchString(part.Path) && !shortcutAssetShape.MatchString(part.Path) {
					return "file must use a saved shortcut attachment"
				}
				if fileMode == "shortcut" && !shortcutAssetShape.MatchString(part.Path) {
					return "file must use a private shortcut attachment"
				}
				if fileMode == "information" && !informationAssetShape.MatchString(part.Path) {
					return "file must use an information library attachment"
				}
				meaningful = true
			case "skill":
				if !ValidInputSkillName(part.Name) || part.Text != "" || part.Path != "" || part.AsFile || part.Favicon != "" {
					return "invalid skill reference"
				}
				meaningful = true
			case "mcp":
				if !ValidInputMCPReference(part.Server, part.Name) || part.Text != "" || part.Path != "" || part.AsFile || part.Favicon != "" {
					return "invalid MCP reference"
				}
				meaningful = true
			case "shortcut":
				if !ValidInputShortcut(part) {
					return "invalid shortcut snapshot"
				}
				if depth >= 5 {
					return "shortcut nesting exceeds 5 levels"
				}
				if ancestors[part.ShortcutID] {
					return "shortcut nesting contains a cycle"
				}
				if part.Parts != nil {
					if len(part.Parts) == 0 {
						return "shortcut content is empty"
					}
					ancestors[part.ShortcutID] = true
					if err := visit(part.Parts, depth+1); err != "" {
						return err
					}
					if !inputPartsMeaningful(part.Parts) {
						return "shortcut content is empty"
					}
					delete(ancestors, part.ShortcutID)
				}
				meaningful = true
			case "office":
				if part.Text != "" || part.Path != "" || part.Name != "" || part.AsFile || part.Favicon != "" || !ValidOfficeSnapshot(part.Office) {
					return "invalid Office selection reference"
				}
				meaningful = true
			default:
				return "unsupported ordered input type"
			}
		}
		return ""
	}
	if err := visit(parts, 0); err != "" {
		return err
	}
	if !meaningful {
		return "ordered input is empty"
	}
	return ""
}

var shortcutAssetShape = regexp.MustCompile(`^shortcut-assets/[a-f0-9]{32}/[^/]+$`)

func ValidInputRelativePath(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\\\x00") && !path.IsAbs(value) && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../") && utf8.ValidString(value)
}

func ValidOfficeSnapshot(o *inputref.OfficeSnapshot) bool {
	if o == nil || !ValidInputRelativePath(o.Path) || (o.Editor != "word" && o.Editor != "cell") || strings.TrimSpace(o.Title) == "" || len(o.Title) > 1024 || strings.TrimSpace(o.Text) == "" || len(o.Text) > 64<<10 || o.CanvasVersion == "" || len(o.CanvasVersion) > 512 || len(o.Sheet) > 512 || len(o.Range) > 512 || len(o.CapturedAt) > 128 || len(o.SourceSession) > 512 {
		return false
	}
	for _, text := range []string{o.Title, o.Text, o.Sheet, o.Range, o.CanvasVersion, o.CapturedAt, o.SourceSession} {
		if !utf8.ValidString(text) {
			return false
		}
		for _, r := range text {
			if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
				return false
			}
		}
	}
	if o.Locator != nil && (o.Editor != "word" || o.Locator.Kind != "text" || o.Locator.Exact == "" || len(o.Locator.Exact) > 64<<10 || len(o.Locator.Prefix) > 4096 || len(o.Locator.Suffix) > 4096 || !utf8.ValidString(o.Locator.Exact+o.Locator.Prefix+o.Locator.Suffix)) {
		return false
	}
	if origin := o.Information; origin != nil {
		if !informationOriginID.MatchString(origin.EntryID) || origin.Version < 1 || origin.Version > 1<<53-1 || !informationDigest.MatchString(origin.SnapshotSHA256) || !informationAssetShape.MatchString(o.Path) || !informationDiskVersion.MatchString(o.CanvasVersion) || o.CanvasVersion != "disk:"+strings.Split(o.Path, "/")[1] || o.Dirty || o.DirtyEpoch != 0 || o.SourceSession != "" {
			return false
		}
	}
	return true
}

func OfficeInputText(o *inputref.OfficeSnapshot) string {
	location := o.Path
	if o.Sheet != "" {
		location += " · " + o.Sheet
	}
	if o.Range != "" {
		location += "!" + o.Range
	}
	header := "[Office选区：" + o.Title + " · " + location + "；引用时快照；版本 " + o.CanvasVersion
	if origin := o.Information; origin != nil {
		header += "；来源 信息库 " + origin.EntryID + "；保存版本 " + strconv.FormatInt(origin.Version, 10) + "；只读选取；资料快照 " + origin.SnapshotSHA256
	}
	if o.Dirty {
		header += "；包含未保存编辑"
	}
	return header + "]\n" + o.Text
}

// ValidInputElementFavicon accepts only optional bounded raster image bytes.
// No remote/file URL or SVG document can make a saved reference fetch content.
func ValidInputElementFavicon(value string) bool {
	if value == "" {
		return true
	}
	const limit = 64 << 10
	if len(value) > base64.StdEncoding.EncodedLen(limit)+64 {
		return false
	}
	prefix, encoded, ok := strings.Cut(value, ";base64,")
	if !ok {
		return false
	}
	switch prefix {
	case "data:image/png", "data:image/jpeg", "data:image/gif", "data:image/webp", "data:image/x-icon", "data:image/vnd.microsoft.icon":
	default:
		return false
	}
	if strings.ContainsAny(encoded, "\r\n") || len(encoded) > base64.StdEncoding.EncodedLen(limit) {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	return err == nil && len(decoded) > 0 && len(decoded) <= limit
}

// ValidInputSkillName validates a reference, not a permission. The live session
// must independently resolve this exact name in its selected skill catalog.
func ValidInputSkillName(name string) bool {
	// Discovery warns about nonstandard names but still loads them. An input
	// reference must not break a previously selectable external skill. Match
	// the editor's bounded atom shape; the session catalog remains authority.
	if name == "" || name == "compact" || !utf8.ValidString(name) || len(utf16.Encode([]rune(name))) > 128 {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) || r == '\ufeff' || strings.ContainsRune("\"'`<>/\\", r) {
			return false
		}
	}
	return true
}

func inputPartsMeaningful(parts []StarterInputPart) bool {
	for _, p := range parts {
		switch p.Type {
		case "text":
			if strings.TrimSpace(p.Text) != "" {
				return true
			}
		case "shortcut":
			if p.Parts != nil {
				if inputPartsMeaningful(p.Parts) {
					return true
				}
			} else if strings.TrimSpace(p.Text) != "" {
				return true
			}
		default:
			return true
		}
	}
	return false
}
