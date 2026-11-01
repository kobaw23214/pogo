package skills

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// PackageMetadata validates the required Agent Skills frontmatter for uploads.
// Discovery remains warning-tolerant; publishing a ZIP uses the strict format.
// directory is empty when SKILL.md is at the ZIP root, otherwise it is the
// single enclosing directory name from the archive, never a staging path.
func PackageMetadata(data []byte, directory string) (name, description string, err error) {
	if len(data) > 256<<10 || !utf8.Valid(data) {
		return "", "", errors.New("SKILL.md must be UTF-8 and at most 256 KiB")
	}
	fields, _ := parseFrontmatter(string(data))
	if fields == nil {
		return "", "", errors.New("SKILL.md requires YAML frontmatter with name and description")
	}
	name, description = fields["name"], fields["description"]
	if len(name) < 1 || len(name) > MaxNameLength || !nameRE.MatchString(name) ||
		strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return "", "", errors.New("Skill name must be 1–64 lowercase letters, digits or hyphens, without leading, trailing or consecutive hyphens")
	}
	if directory != "" && directory != name {
		return "", "", fmt.Errorf("Skill directory %q must match frontmatter name %q", directory, name)
	}
	if strings.TrimSpace(description) == "" || utf8.RuneCountInString(description) > MaxDescriptionLength {
		return "", "", errors.New("Skill description must contain 1–1024 characters")
	}
	if compatibility, exists := fields["compatibility"]; exists && (strings.TrimSpace(compatibility) == "" || utf8.RuneCountInString(compatibility) > 500) {
		return "", "", errors.New("Skill compatibility must contain 1–500 characters when provided")
	}
	return name, description, nil
}
