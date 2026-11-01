package web

// Agent markdown role-card import: paste one document, get one Studio agent.
// The frontmatter is the skills package's hand-written subset (same parser —
// still no YAML library), the body becomes the agent's instructions. Missing
// name is an error; a name that already exists is a conflict, never a silent
// overwrite.
import (
	"errors"
	"net/http"
	"strings"

	"github.com/yosukeno/pogo/skills"
)

func (s *Server) handleStudioImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Markdown string `json:"markdown"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	md := strings.TrimSpace(body.Markdown)
	if md == "" {
		writeErr(w, 400, errors.New("markdown 不能为空"))
		return
	}
	if len(md) > 256<<10 {
		writeErr(w, 400, errors.New("markdown 超过 256 KiB 限制"))
		return
	}
	fields, bodyText := skills.ParseFrontmatter(md)
	if fields == nil {
		writeErr(w, 400, errors.New("缺少 YAML frontmatter（--- 围栏的 name/description 头）"))
		return
	}
	name := strings.TrimSpace(fields["name"])
	if name == "" {
		writeErr(w, 400, errors.New("frontmatter 缺少 name"))
		return
	}
	for _, a := range s.mgr.studio.list(true) {
		if a.Name == name {
			writeErr(w, 409, errors.New("已存在同名 Agent："+name+"，请改名后导入"))
			return
		}
	}
	in := studioInput{
		Name:         name,
		Description:  strings.TrimSpace(fields["description"]),
		Instructions: strings.TrimSpace(bodyText),
		Model:        strings.TrimSpace(fields["model"]),
		Status:       "ready",
	}
	if in.Model == "" {
		in.Model = s.mgr.cfg.Model
	}
	if tools := strings.TrimSpace(fields["tools"]); tools != "" {
		list := []string{}
		for _, entry := range strings.Split(tools, ",") {
			if entry = strings.TrimSpace(entry); entry != "" {
				list = append(list, entry)
			}
		}
		in.Tools = &list
	}
	a, err := s.mgr.studio.put("", in, 0)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, a)
}
