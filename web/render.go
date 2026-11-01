package web

// Office-document rendering for the preview tab. A browser can show an image
// or a PDF on its own, but docx and xlsx have no native viewer — so the server
// converts them to markdown here, the same stdlib-only parsing the docs skill
// does for the model (zip + XML walk, headings and tables for docx, shared
// strings and cell refs for xlsx). Read-only, bounded, no exec: the endpoint
// answers with text or a plain error, never with the original bytes.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// renderMaxBytes caps the markdown a preview returns. Same order as the
	// docs skill's 120k budget: enough to read a real report, not enough to
	// wedge the tab on a generated monster.
	renderMaxBytes = 120 << 10
	// renderMaxSource bounds a single zip member — a preview never needs to
	// hold more than the upload channel itself accepts.
	renderMaxSource = 25 << 20
	// sheetRowCap / sheetColCap / sheetSheetCap bound an xlsx preview: the
	// first screenful of each sheet is what a preview is for; the docs skill
	// remains the way to read a whole workbook.
	sheetRowCap   = 80
	sheetColCap   = 30
	sheetSheetCap = 20
)

// handleFileRender converts a docx or xlsx to markdown for the preview tab.
// Every other extension is a 415 with a specific message — the client only
// calls this for the two it cannot render itself, so a stray call deserves a
// plain answer rather than a generic one.
func (s *Server) handleFileRender(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.resolvePanelPath(w, r)
	if !ok {
		return
	}
	var text string
	var truncated bool
	var err error
	switch strings.ToLower(filepath.Ext(abs)) {
	case ".docx":
		text, truncated, err = renderDocx(abs)
	case ".xlsx", ".xlsm":
		text, truncated, err = renderXlsx(abs)
	case ".doc":
		err = errors.New("旧版 .doc 为二进制格式，无法在线预览，请下载后查看")
	case ".xls":
		err = errors.New("旧版 .xls 为二进制格式，无法在线预览，请下载后查看")
	default:
		err = errors.New("此格式无需服务端渲染（图片/PDF/文本由浏览器直接预览）")
	}
	if err != nil {
		writeErr(w, http.StatusUnsupportedMediaType, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      r.URL.Query().Get("path"),
		"text":      text,
		"truncated": truncated,
	})
}

// openZipMember reads one member of a zip file, bounded by renderMaxSource.
func openZipMember(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		if f.UncompressedSize64 > renderMaxSource {
			return nil, fmt.Errorf("%s 过大（%d MB），超出预览上限", name, f.UncompressedSize64>>20)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, renderMaxSource+1))
		if err != nil {
			return nil, err
		}
		if len(b) > renderMaxSource {
			return nil, fmt.Errorf("%s 过大，超出预览上限", name)
		}
		return b, nil
	}
	return nil, fmt.Errorf("压缩包内缺少 %s（文件可能已损坏）", name)
}

// clipRender trims a truncated render to the budget on a line boundary, so the
// preview's last visible line ends cleanly; the "truncated" flag in the
// response is what tells the client to say why.
func clipRender(s string) string {
	if len(s) > renderMaxBytes {
		s = s[:renderMaxBytes]
		if i := strings.LastIndexByte(s, '\n'); i > 0 {
			s = s[:i]
		}
		s += "\n\n…\n"
	}
	return s
}

// writeMarkdownTable emits a GFM table, first row as the header (the same
// convention as the skill's xlsx-sheet). Pipes and newlines in cell text are
// escaped; an all-empty table is skipped. The byte budget is checked once per
// row so the walk stops early rather than after building the whole string.
func writeMarkdownTable(out *strings.Builder, rows [][]string, truncated *bool) {
	cols := 0
	anyText := false
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
		for _, c := range r {
			if strings.TrimSpace(c) != "" {
				anyText = true
			}
		}
	}
	if cols == 0 || !anyText {
		return
	}
	cell := func(r []string, i int) string {
		if i >= len(r) {
			return ""
		}
		t := strings.ReplaceAll(strings.TrimSpace(r[i]), "|", "\\|")
		return strings.ReplaceAll(t, "\n", " ")
	}
	var line strings.Builder
	for i := 0; i < cols; i++ {
		line.WriteString("| " + cell(rows[0], i) + " ")
	}
	out.WriteString(line.String() + "|\n")
	out.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
	for _, r := range rows[1:] {
		line.Reset()
		for i := 0; i < cols; i++ {
			line.WriteString("| " + cell(r, i) + " ")
		}
		out.WriteString(line.String() + "|\n")
		if out.Len() >= renderMaxBytes {
			*truncated = true
			return
		}
	}
	out.WriteString("\n")
}

// ── docx → markdown ─────────────────────────────────────────────────────────
// The walk mirrors the docs skill's docx-text: paragraphs in body order,
// heading levels off w:pStyle ("HeadingN"/"标题 N"), tables as GFM. Block
// anchors and other model-facing furniture stay skill-side — a preview reads,
// it does not cite.

// docxPara is one accumulated paragraph; a table cell holds these too, joined
// with spaces into the cell's text.
type docxPara struct {
	style    string
	numbered bool
	text     strings.Builder
}

func (p *docxPara) headingLevel() int {
	s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p.style), " ", ""))
	if s == "title" {
		return 1
	}
	if s == "subtitle" {
		return 2
	}
	for _, prefix := range []string{"heading", "标题"} {
		if strings.HasPrefix(s, prefix) {
			if n, err := strconv.Atoi(strings.TrimPrefix(s, prefix)); err == nil && n >= 1 && n <= 6 {
				return n
			}
		}
	}
	return 0
}

func renderDocx(abs string) (string, bool, error) {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return "", false, errors.New("不是有效的 docx（无法作为 zip 打开）")
	}
	defer zr.Close()
	return renderDocxReader(&zr.Reader)
}

// renderDocxReader also accepts ZIPs created from an already-authorized file
// descriptor, without reopening a pathname that could have become a symlink.
func renderDocxReader(zr *zip.Reader) (string, bool, error) {
	doc, err := openZipMember(zr, "word/document.xml")
	if err != nil {
		return "", false, err
	}

	var out strings.Builder
	truncated := false
	// inCell says where the current paragraph's text belongs: body paragraphs
	// flush as their own lines, cell paragraphs fold into the current cell's
	// entry (the last one appended to the row).
	var inCell bool
	var table [][]string
	var row []string
	var para *docxPara
	// inT/inInstr gate which character data is content: w:t holds text,
	// instrText holds field instructions nobody wants to read.
	var inT, inInstr bool

	appendText := func(s string) {
		if para != nil {
			para.text.WriteString(s)
		}
	}
	flushPara := func() {
		if truncated || para == nil {
			return
		}
		p := para
		para = nil
		text := strings.TrimRight(p.text.String(), " \t\n")
		if inCell {
			if len(row) == 0 {
				row = append(row, "")
			}
			if row[len(row)-1] != "" && text != "" {
				row[len(row)-1] += " "
			}
			row[len(row)-1] += text
			return
		}
		if text == "" {
			return
		}
		var line string
		switch {
		case p.headingLevel() > 0:
			line = strings.Repeat("#", p.headingLevel()) + " " + text
		case p.numbered:
			line = "- " + text
		default:
			line = text
		}
		out.WriteString(line + "\n\n")
		if out.Len() >= renderMaxBytes {
			truncated = true
		}
	}

	dec := xml.NewDecoder(bytes.NewReader(doc))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false, errors.New("document.xml 解析失败（文件可能已损坏）")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				para = &docxPara{}
			case "tbl":
				table = [][]string{}
			case "tr":
				row = []string{}
			case "tc":
				inCell = true
				row = append(row, "")
			case "pStyle":
				if para != nil && len(t.Attr) > 0 {
					para.style = t.Attr[0].Value
				}
			case "numPr":
				if para != nil {
					para.numbered = true
				}
			case "t":
				inT = true
			case "instrText":
				inInstr = true
			case "tab":
				appendText("\t")
			case "br", "cr":
				appendText("\n")
			}
		case xml.CharData:
			if inT && !inInstr {
				appendText(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "instrText":
				inInstr = false
			case "p":
				flushPara()
			case "tc":
				inCell = false
			case "tr":
				if table != nil {
					table = append(table, row)
					row = nil
				}
			case "tbl":
				writeMarkdownTable(&out, table, &truncated)
				table = nil
			}
		}
	}
	if truncated {
		return clipRender(out.String()), true, nil
	}
	return out.String(), false, nil
}

// ── xlsx → markdown ─────────────────────────────────────────────────────────
// Sheet names and their targets come from workbook.xml + its rels (the honest
// order); shared strings are resolved so t="s" cells read as text; empty cells
// stay "" — never NaN, markitdown's lesson carried over from the skill.

type sheetRef struct {
	name   string
	target string // zip member path, e.g. xl/worksheets/sheet1.xml
}

func renderXlsx(abs string) (string, bool, error) {
	zr, err := zip.OpenReader(abs)
	if err != nil {
		return "", false, errors.New("不是有效的 xlsx（无法作为 zip 打开）")
	}
	defer zr.Close()
	return renderXlsxReader(&zr.Reader)
}

func renderXlsxReader(zr *zip.Reader) (string, bool, error) {
	sheets, err := xlsxSheets(zr)
	if err != nil {
		return "", false, err
	}
	shared := xlsxSharedStrings(zr)

	var out strings.Builder
	truncated := false
	for i, sh := range sheets {
		if i >= sheetSheetCap || out.Len() >= renderMaxBytes {
			truncated = true
			break
		}
		doc, err := openZipMember(zr, sh.target)
		if err != nil {
			continue // a damaged sheet must not take the whole preview down
		}
		rows, rowsCut := xlsxSheetRows(doc, shared)
		out.WriteString(fmt.Sprintf("## %s（%d 行）\n\n", sh.name, len(rows)))
		writeMarkdownTable(&out, rows, &truncated)
		if rowsCut {
			out.WriteString("\n（仅显示前 " + strconv.Itoa(sheetRowCap) + " 行）\n\n")
		}
	}
	if truncated {
		return clipRender(out.String()), true, nil
	}
	return out.String(), false, nil
}

// xlsxSheets lists sheets in workbook order, resolving each r:id through the
// workbook rels. Without rels (or with unresolvable ids) it falls back to the
// plain sheetN members in numeric order — the common layout.
func xlsxSheets(zr *zip.Reader) ([]sheetRef, error) {
	wb, err := openZipMember(zr, "xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	type rawSheet struct {
		name string
		rid  string
	}
	var sheets []rawSheet
	dec := xml.NewDecoder(bytes.NewReader(wb))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("workbook.xml 解析失败")
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			var name, rid string
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "name":
					name = a.Value
				case "id":
					rid = a.Value
				}
			}
			sheets = append(sheets, rawSheet{name, rid})
		}
	}
	if len(sheets) == 0 {
		return nil, errors.New("工作簿中没有工作表")
	}

	targets := map[string]string{}
	if rels, err := openZipMember(zr, "xl/_rels/workbook.xml.rels"); err == nil {
		dec := xml.NewDecoder(bytes.NewReader(rels))
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
				var id, target string
				for _, a := range se.Attr {
					switch a.Name.Local {
					case "Id":
						id = a.Value
					case "Target":
						target = a.Value
					}
				}
				if id != "" && strings.Contains(target, "worksheet") && strings.HasSuffix(target, ".xml") {
					targets[id] = relTarget(target)
				}
			}
		}
	}

	var out []sheetRef
	for _, s := range sheets {
		if t, ok := targets[s.rid]; ok {
			out = append(out, sheetRef{name: s.name, target: t})
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	// Fallback: numeric order over the plain sheetN members themselves.
	var members []string
	for _, f := range zr.File {
		base := strings.TrimPrefix(f.Name, "xl/worksheets/")
		if strings.HasPrefix(f.Name, "xl/worksheets/") && strings.HasSuffix(base, ".xml") &&
			numericName(strings.TrimSuffix(base, ".xml")) {
			members = append(members, f.Name)
		}
	}
	sort.Slice(members, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(members[i], "xl/worksheets/"), ".xml"))
		b, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(members[j], "xl/worksheets/"), ".xml"))
		return a < b
	})
	if len(members) == 0 {
		return nil, errors.New("工作簿中没有可读取的工作表")
	}
	for i, m := range members {
		name := "Sheet" + strconv.Itoa(i+1)
		if i < len(sheets) && sheets[i].name != "" {
			name = sheets[i].name
		}
		out = append(out, sheetRef{name: name, target: m})
	}
	return out, nil
}

// relTarget normalises a rels Target to a zip member path: most writers use
// "worksheets/sheet1.xml" relative to xl/, some the absolute "/xl/worksheets/…".
func relTarget(t string) string {
	t = strings.TrimPrefix(t, "/")
	if !strings.HasPrefix(t, "xl/") {
		t = "xl/" + t
	}
	return t
}

// numericName says whether s is all digits (a plain sheetN member).
func numericName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// xlsxSharedStrings collects the shared-string table: each <si> concatenates
// its direct and rich-run <t> text.
func xlsxSharedStrings(zr *zip.Reader) []string {
	b, err := openZipMember(zr, "xl/sharedStrings.xml")
	if err != nil {
		return nil
	}
	var shared []string
	var cur strings.Builder
	var inSI, inT bool
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err != nil {
			return shared
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = inSI
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				inSI = false
				shared = append(shared, cur.String())
			}
		}
	}
}

// xlsxSheetRows decodes one worksheet: sparse cells placed by their column
// letters (r="C5" → col 2), shared strings resolved, values trimmed to the
// caps. The second return says the row cap cut the sheet short.
func xlsxSheetRows(doc []byte, shared []string) ([][]string, bool) {
	var rows [][]string
	var row []string
	var inV, inIS bool
	var cellType, cellRef string
	var val strings.Builder
	cut := false

	dec := xml.NewDecoder(bytes.NewReader(doc))
	for {
		tok, err := dec.Token()
		if err != nil {
			break // a malformed tail keeps the rows already read
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = []string{}
				cellRef = ""
			case "c":
				cellType, cellRef = "", ""
				val.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						cellRef = a.Value
					case "t":
						cellType = a.Value
					}
				}
			case "v":
				inV = true
			case "is":
				inIS = true
			case "t":
				if inIS {
					val.Reset()
					inV = true
				}
			}
		case xml.CharData:
			if inV {
				val.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "is":
				inIS = false
			case "t":
				if inIS {
					inV = false
				}
			case "c":
				switch cellType {
				case "s":
					if n, err := strconv.Atoi(strings.TrimSpace(val.String())); err == nil && n >= 0 && n < len(shared) {
						setCell(&row, cellRef, strings.ReplaceAll(shared[n], "\n", " "))
					}
				case "inlineStr":
					setCell(&row, cellRef, strings.TrimSpace(val.String()))
				case "b":
					if strings.TrimSpace(val.String()) == "1" {
						setCell(&row, cellRef, "TRUE")
					} else {
						setCell(&row, cellRef, "FALSE")
					}
				default: // "str" formulas and plain numbers
					setCell(&row, cellRef, strings.TrimSpace(val.String()))
				}
			case "row":
				if len(rows) < sheetRowCap {
					rows = append(rows, row)
				} else {
					cut = true
				}
				row = nil
			}
		}
	}
	return rows, cut
}

// setCell places text at the cell's column, growing the sparse row with ""
// (never NaN) and clipping past the column cap.
func setCell(row *[]string, ref, text string) {
	col := colIndexOf(ref)
	for len(*row) <= col {
		*row = append(*row, "")
	}
	if col < sheetColCap {
		(*row)[col] = text
	}
}

// colIndexOf reads the column letters of a cell reference ("BC12" → 1-based
// 55 → 0-based 54). An empty or malformed ref falls back to column zero —
// better one misplaced cell than a lost one.
func colIndexOf(ref string) int {
	n := 0
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case c >= 'A' && c <= 'Z':
			n = n*26 + int(c-'A') + 1
		case c >= 'a' && c <= 'z':
			n = n*26 + int(c-'a') + 1
		default:
			return max(n-1, 0)
		}
	}
	return max(n-1, 0)
}
