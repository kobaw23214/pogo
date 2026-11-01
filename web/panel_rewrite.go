package web

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"html"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/css"
	htmllex "github.com/tdewolff/parse/v2/html"
	"github.com/tdewolff/parse/v2/js"
)

//go:embed panel_compat_bootstrap.js
var panelCompatBootstrap string

type panelHTMLToken struct {
	kind             htmllex.TokenType
	raw, name, value string
}

func unquotePanelValue(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// Tokenize markup, never search-and-replace arbitrary text or script strings.
// The first base element applies to all document URLs, including earlier ones.
func rewritePanelHTML(body []byte, m panelURLMapper, page *url.URL, nonce string) ([]byte, error) {
	l := htmllex.NewLexer(parse.NewInputBytes(body))
	var tokens []panelHTMLToken
	base := page
	baseFound, tag := false, ""
	for {
		kind, raw := l.Next()
		if kind == htmllex.ErrorToken {
			if l.Err() != io.EOF {
				return nil, l.Err()
			}
			break
		}
		t := panelHTMLToken{kind: kind, raw: string(raw)}
		if kind == htmllex.StartTagToken {
			tag = strings.ToLower(string(l.Text()))
			t.name = tag
		}
		if kind == htmllex.AttributeToken {
			t.name, t.value = strings.ToLower(string(l.AttrKey())), html.UnescapeString(unquotePanelValue(string(l.AttrVal())))
			if tag == "base" && t.name == "href" && !baseFound {
				if u, err := page.Parse(t.value); err == nil {
					base = u
					baseFound = true
				}
			}
		}
		tokens = append(tokens, t)
	}
	var out bytes.Buffer
	injected := false
	inline := ""
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.kind == htmllex.StartTagToken {
			attrs := map[string]string{}
			end := i + 1
			for end < len(tokens) && tokens[end].kind == htmllex.AttributeToken {
				attrs[tokens[end].name] = tokens[end].value
				end++
			}
			// Do not remove integrity/CSP protections just to make a page load.
			if attrs["integrity"] != "" && (t.name == "script" || t.name == "link") {
				ref := attrs["src"]
				if t.name == "link" {
					ref = attrs["href"]
				}
				if m.rewrite(ref, base) != ref {
					return nil, panelUnsupported("Integrity-protected local resources need original forwarding or an independent browser origin.")
				}
			}
			if !injected && t.name != "html" && t.name != "head" {
				out.WriteString(panelBootstrap(m, page, nonce))
				injected = true
			}
			out.WriteString(t.raw)
			for j := i + 1; j < end; j++ {
				a := tokens[j]
				value := a.value
				switch a.name {
				case "src", "href", "action", "formaction", "poster", "data":
					refBase := base
					if t.name == "base" {
						refBase = page
					}
					value = m.rewrite(value, refBase)
				case "style":
					v, err := rewritePanelCSS([]byte(value), m, base)
					if err != nil {
						return nil, err
					}
					value = string(v)
				case "srcset", "imagesrcset":
					value = rewritePanelSrcset(value, m, base)
				case "content":
					if t.name == "meta" && strings.EqualFold(attrs["http-equiv"], "Content-Security-Policy") {
						value = panelCSPNonce(value, nonce)
					}
					if t.name == "meta" && strings.EqualFold(attrs["http-equiv"], "refresh") {
						value = rewritePanelRefresh(value, m, base)
					}
				}
				if value != a.value {
					out.WriteString(" " + a.name + `="` + html.EscapeString(value) + `"`)
				} else {
					out.WriteString(a.raw)
				}
			}
			if end < len(tokens) {
				out.WriteString(tokens[end].raw)
			}
			if t.name == "head" && !injected {
				out.WriteString(panelBootstrap(m, page, nonce))
				injected = true
			}
			inline = ""
			if t.name == "style" {
				inline = "css"
			}
			if t.name == "script" && attrs["src"] == "" {
				inline = strings.ToLower(attrs["type"])
			}
			i = end
			continue
		}
		if t.kind == htmllex.EndTagToken {
			inline = ""
		}
		if t.kind == htmllex.TextToken {
			var changed []byte
			var err error
			switch inline {
			case "css":
				changed, err = rewritePanelCSS([]byte(t.raw), m, base)
			case "module":
				changed, err = rewritePanelJS([]byte(t.raw), m, base)
			case "importmap":
				changed, err = rewritePanelImportMap([]byte(t.raw), m, base)
			}
			if err != nil {
				return nil, err
			}
			if changed != nil {
				out.Write(changed)
				continue
			}
		}
		out.WriteString(t.raw)
	}
	if !injected {
		out.WriteString(panelBootstrap(m, page, nonce))
	}
	return out.Bytes(), nil
}

func rewritePanelRefresh(value string, m panelURLMapper, base *url.URL) string {
	i := strings.Index(value, ";")
	if i < 0 {
		return value
	}
	tail := strings.TrimSpace(value[i+1:])
	key, ref, ok := strings.Cut(tail, "=")
	if !ok || !strings.EqualFold(strings.TrimSpace(key), "url") {
		return value
	}
	return value[:i+1] + "url=" + m.rewrite(unquotePanelValue(strings.TrimSpace(ref)), base)
}

func rewritePanelSrcset(value string, m panelURLMapper, base *url.URL) string {
	// Commas inside a data URL belong to the URL, not the candidate separator.
	var candidates []string
	for s := strings.TrimSpace(value); s != ""; {
		s = strings.TrimLeft(s, " \t\r\n,")
		if s == "" {
			break
		}
		i := strings.IndexAny(s, " \t\r\n")
		if i < 0 {
			i = len(s)
		}
		ref := s[:i]
		s = s[i:]
		if strings.HasSuffix(ref, ",") {
			candidates = append(candidates, m.rewrite(strings.TrimRight(ref, ","), base))
			continue
		}
		j := strings.IndexByte(s, ',')
		if j < 0 {
			j = len(s)
		}
		descriptor := s[:j]
		s = s[j:]
		candidates = append(candidates, m.rewrite(ref, base)+descriptor)
	}
	return strings.Join(candidates, ", ")
}

func decodePanelCSS(s string) string {
	s = unquotePanelValue(strings.TrimSpace(s))
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			out.WriteByte(s[i])
			continue
		}
		i++
		start := i
		for i < len(s) && i-start < 6 && strings.ContainsRune("0123456789abcdefABCDEF", rune(s[i])) {
			i++
		}
		if i > start {
			n, _ := strconv.ParseInt(s[start:i], 16, 32)
			r := rune(n)
			if r == 0 || !utf8.ValidRune(r) {
				r = utf8.RuneError
			}
			out.WriteRune(r)
			if i < len(s) && strings.ContainsRune(" \t\r\n\f", rune(s[i])) {
				if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
					i++
				}
			} else {
				i--
			}
		} else if s[i] != '\n' && s[i] != '\r' && s[i] != '\f' {
			out.WriteByte(s[i])
		}
	}
	return out.String()
}

func quotePanelCSS(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\a `, "\r", `\d `).Replace(s) + `"`
}

func rewritePanelCSS(body []byte, m panelURLMapper, base *url.URL) ([]byte, error) {
	l := css.NewLexer(parse.NewInputBytes(body))
	var out bytes.Buffer
	importing := false
	for {
		kind, raw := l.Next()
		if kind == css.ErrorToken {
			if l.Err() != io.EOF {
				return nil, l.Err()
			}
			break
		}
		value := string(raw)
		if kind == css.URLToken {
			start := strings.IndexByte(value, '(')
			end := strings.LastIndexByte(value, ')')
			if start >= 0 && end > start {
				ref := decodePanelCSS(value[start+1 : end])
				if mapped := m.rewrite(ref, base); mapped != ref {
					value = "url(" + quotePanelCSS(mapped) + ")"
				}
			}
		} else if importing && kind == css.StringToken {
			ref := decodePanelCSS(value)
			if mapped := m.rewrite(ref, base); mapped != ref {
				value = quotePanelCSS(mapped)
			}
		}
		out.WriteString(value)
		if kind != css.WhitespaceToken && kind != css.CommentToken {
			importing = kind == css.AtKeywordToken && strings.EqualFold(value, "@import")
		}
	}
	return out.Bytes(), nil
}

type panelJSVisitor struct {
	m       panelURLMapper
	base    *url.URL
	changed bool
}

func (v *panelJSVisitor) module(data *[]byte) {
	var b bytes.Buffer
	lit := js.LiteralExpr{TokenType: js.StringToken, Data: *data}
	if lit.JSON(&b) != nil {
		return
	}
	var ref string
	if json.Unmarshal(b.Bytes(), &ref) != nil {
		return
	}
	// Bare module names are import-map keys, not relative URLs.
	if !strings.HasPrefix(ref, "/") && !strings.HasPrefix(ref, "./") && !strings.HasPrefix(ref, "../") && !strings.Contains(ref, ":") {
		return
	}
	if mapped := v.m.rewrite(ref, v.base); mapped != ref {
		*data, _ = json.Marshal(mapped)
		v.changed = true
	}
}
func (v *panelJSVisitor) Enter(node js.INode) js.IVisitor {
	switch n := node.(type) {
	case *js.ImportStmt:
		v.module(&n.Module)
	case *js.ExportStmt:
		if len(n.Module) > 0 {
			v.module(&n.Module)
		}
	case *js.CallExpr:
		if callee, ok := n.X.(*js.LiteralExpr); ok && callee.TokenType == js.ImportToken && len(n.Args.List) > 0 {
			if arg, ok := n.Args.List[0].Value.(*js.LiteralExpr); ok && arg.TokenType == js.StringToken {
				v.module(&arg.Data)
			}
		}
	}
	return v
}
func (v *panelJSVisitor) Exit(js.INode) {}
func rewritePanelJS(body []byte, m panelURLMapper, base *url.URL) ([]byte, error) {
	ast, err := js.Parse(parse.NewInputBytes(body), js.Options{})
	if err != nil {
		return nil, panelUnsupported("The script syntax cannot be safely adapted. Use original forwarding or an independent browser.")
	}
	v := &panelJSVisitor{m: m, base: base}
	js.Walk(v, ast)
	if !v.changed {
		return body, nil
	}
	var out bytes.Buffer
	ast.JS(&out)
	return out.Bytes(), nil
}

func rewritePanelImportMap(body []byte, m panelURLMapper, base *url.URL) ([]byte, error) {
	var data map[string]json.RawMessage
	if json.Unmarshal(body, &data) != nil {
		return nil, panelUnsupported("Invalid import map.")
	}
	rewrite := func(raw json.RawMessage) (json.RawMessage, error) {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		mapped := map[string]json.RawMessage{}
		for key, value := range entries {
			if strings.HasPrefix(key, "/") || strings.HasPrefix(key, ".") || strings.Contains(key, ":") {
				key = m.rewrite(key, base)
			}
			var ref string
			if json.Unmarshal(value, &ref) == nil {
				value, _ = json.Marshal(m.rewrite(ref, base))
			}
			mapped[key] = value
		}
		return json.Marshal(mapped)
	}
	if raw, ok := data["imports"]; ok {
		value, err := rewrite(raw)
		if err != nil {
			return nil, panelUnsupported("Invalid import map entries.")
		}
		data["imports"] = value
	}
	if raw, ok := data["scopes"]; ok {
		var scopes map[string]json.RawMessage
		if json.Unmarshal(raw, &scopes) != nil {
			return nil, panelUnsupported("Invalid import map scopes.")
		}
		mapped := map[string]json.RawMessage{}
		for key, value := range scopes {
			entries, err := rewrite(value)
			if err != nil {
				return nil, panelUnsupported("Invalid import map scope entries.")
			}
			mapped[m.rewrite(key, base)] = entries
		}
		data["scopes"], _ = json.Marshal(mapped)
	}
	return json.Marshal(data)
}
