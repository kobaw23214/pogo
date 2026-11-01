package skillauthor

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yosukeno/pogo/skills"
)

var scopeRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
var idRE = regexp.MustCompile(`^as_[a-f0-9]{32}$`)
var requestRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var markdownInlineLink = regexp.MustCompile(`!?\[[^\]\n]*\]\(\s*(<[^>\n]*>|[^\s)]+)(?:\s+[^)\n]*)?\)`)
var markdownDefinition = regexp.MustCompile(`(?m)^ {0,3}\[[^\]\n]+\]:\s*(<[^>\n]*>|\S+)`)
var markdownCode = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~|`[^`\\n]*`")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func textOK(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' })
}

func validFilePath(p string) bool {
	if !utf8.ValidString(p) || p == "" || len(p) > 512 || strings.ContainsAny(p, "\\\x00%?#:") || path.Clean(p) != p || strings.HasPrefix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") || strings.TrimSpace(part) != part || strings.ContainsFunc(part, unicode.IsControl) {
			return false
		}
	}
	if p == "SKILL.md" {
		return true
	}
	if !strings.HasPrefix(p, "references/") || len(strings.Split(p, "/")) > 10 {
		return false
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".txt", ".json", ".yaml", ".yml", ".sql", ".csv", ".tsv":
		return true
	}
	return false
}

// ValidateFiles checks package structure and local document links only. It does
// not execute, behavior-test, install, or grant any tool named in the text.
func ValidateFiles(files []File) (name, description string, err error) {
	if len(files) == 0 || len(files) > MaxFiles {
		return "", "", fmt.Errorf("%w: expected 1–%d files", ErrLimit, MaxFiles)
	}
	paths := make(map[string]bool, len(files))
	folded := make(map[string]bool, len(files))
	total := 0
	var manifest string
	for _, f := range files {
		if !validFilePath(f.Path) || !textOK(f.Content) {
			return "", "", invalid("unsupported path or non-text content: %q", f.Path)
		}
		lower := strings.ToLower(f.Path)
		if folded[lower] {
			return "", "", invalid("duplicate file path: %q", f.Path)
		}
		folded[lower] = true
		paths[f.Path] = true
		if len(f.Content) > MaxFileBytes {
			return "", "", fmt.Errorf("%w: file %q exceeds %d bytes", ErrLimit, f.Path, MaxFileBytes)
		}
		total += len(f.Content)
		if total > MaxSkillBytes {
			return "", "", fmt.Errorf("%w: Skill exceeds %d bytes", ErrLimit, MaxSkillBytes)
		}
		if f.Path == "SKILL.md" {
			manifest = f.Content
		}
	}
	for p := range paths {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if folded[strings.ToLower(parent)] {
				return "", "", invalid("file/directory overlap at %q", parent)
			}
		}
	}
	if manifest == "" {
		return "", "", invalid("exactly one nonempty SKILL.md is required")
	}
	if err = validateFrontmatter(manifest); err != nil {
		return "", "", err
	}
	name, description, err = skills.PackageMetadata([]byte(manifest), "")
	if err != nil {
		return "", "", invalid("%s", err)
	}
	_, body := skills.ParseFrontmatter(manifest)
	if strings.TrimSpace(body) == "" {
		return "", "", invalid("SKILL.md requires workflow instructions")
	}
	for _, f := range files {
		if strings.EqualFold(path.Ext(f.Path), ".md") {
			if err = validateLinks(f, paths); err != nil {
				return "", "", err
			}
		}
	}
	return name, description, nil
}

func validateFrontmatter(raw string) error {
	text := strings.TrimPrefix(strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n"), "\ufeff")
	if !strings.HasPrefix(text, "---\n") {
		return invalid("SKILL.md requires complete frontmatter")
	}
	seen := map[string]bool{}
	block := false
	closed := false
	for _, line := range strings.Split(text, "\n")[1:] {
		trim := strings.TrimSpace(line)
		if line == "---" || line == "..." {
			closed = true
			break
		}
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if !block {
				return invalid("nested frontmatter is unsupported")
			}
			continue
		}
		block = false
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || seen[key] {
			return invalid("invalid or duplicate frontmatter field %q", key)
		}
		switch key {
		case "name", "description", "license", "compatibility":
		default:
			return invalid("unsupported frontmatter field %q; instruction Skills cannot grant capabilities", key)
		}
		seen[key] = true
		if value == "" {
			return invalid("frontmatter %s must be a scalar", key)
		}
		if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
			if value != "|" && value != "|-" && value != "|+" && value != ">" && value != ">-" && value != ">+" {
				return invalid("unsupported block scalar")
			}
			block = true
			continue
		}
		if strings.ContainsAny(value[:1], "[{}]&*!%") {
			return invalid("unsupported frontmatter scalar")
		}
		if value[0] == '"' {
			if _, err := strconv.Unquote(value); err != nil {
				return invalid("invalid quoted frontmatter scalar")
			}
		}
		if value[0] == '\'' {
			if len(value) < 2 || value[len(value)-1] != '\'' || strings.Contains(strings.ReplaceAll(value[1:len(value)-1], "''", ""), "'") {
				return invalid("invalid quoted frontmatter scalar")
			}
		}
	}
	if !closed || !seen["name"] || !seen["description"] {
		return invalid("frontmatter requires name, description and closing separator")
	}
	return nil
}

func validateLinks(f File, paths map[string]bool) error {
	content := markdownCode.ReplaceAllString(f.Content, "")
	for _, re := range []*regexp.Regexp{markdownInlineLink, markdownDefinition} {
		for _, match := range re.FindAllStringSubmatch(content, -1) {
			dest := strings.TrimSuffix(strings.TrimPrefix(match[1], "<"), ">")
			u, err := url.Parse(dest)
			if err != nil {
				return invalid("invalid link in %s", f.Path)
			}
			if u.IsAbs() {
				if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "mailto" {
					return invalid("unsupported link scheme in %s", f.Path)
				}
				continue
			}
			if u.Host != "" || strings.HasPrefix(dest, "/") {
				return invalid("absolute document links are unsupported")
			}
			if u.Path == "" {
				continue
			}
			p, err := url.PathUnescape(u.EscapedPath())
			if err != nil || strings.Contains(p, "\\") {
				return invalid("invalid local link in %s", f.Path)
			}
			target := path.Clean(path.Join(path.Dir(f.Path), p))
			if !paths[target] {
				return invalid("local link %q in %s does not name a bundled document", dest, f.Path)
			}
		}
	}
	return nil
}
