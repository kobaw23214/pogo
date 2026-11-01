package web

// The version ledger compares a deliberately narrow, bounded projection of an
// OOXML package. It is not a renderer, a fidelity check or a replacement for
// checking the live editor. Unsupported content is reported as a limitation.
import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

const officeVersionDiffChanges = 40
const officeVersionDiffPartBytes = 8 << 20
const officeVersionDiffTotalBytes = 32 << 20
const officeVersionDiffCells = 100000
const officeVersionDiffParagraphs = 20000

type officeVersionChange struct {
	Kind    string `json:"kind"`
	Address string `json:"address,omitempty"`
	Sheet   string `json:"sheet,omitempty"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

type officeVersionDiff struct {
	Coverage    string                `json:"coverage"`
	ChangeCount int                   `json:"changeCount"`
	Changes     []officeVersionChange `json:"changes"`
	Truncated   bool                  `json:"truncated"`
	Limitations []string              `json:"limitations"`
}

type officeVersionPackage struct {
	files map[string]*zip.File
	read  int
}

func officeVersionPackageFor(data []byte) (*officeVersionPackage, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(r.File) > 4096 {
		return nil, errors.New("invalid package")
	}
	p := &officeVersionPackage{files: map[string]*zip.File{}}
	for _, f := range r.File {
		if p.files[f.Name] != nil {
			return nil, errors.New("duplicate package part")
		}
		p.files[f.Name] = f
	}
	return p, nil
}

func (p *officeVersionPackage) part(name string) ([]byte, error) {
	f := p.files[name]
	if f == nil {
		return nil, errors.New("missing package part")
	}
	if f.UncompressedSize64 > officeVersionDiffPartBytes || p.read+int(f.UncompressedSize64) > officeVersionDiffTotalBytes {
		return nil, errors.New("comparison limit")
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, officeVersionDiffPartBytes+1))
	if err != nil || len(data) > officeVersionDiffPartBytes || p.read+len(data) > officeVersionDiffTotalBytes {
		return nil, errors.New("comparison limit")
	}
	p.read += len(data)
	return data, nil
}

func officeVersionXML(data []byte, visit func(xml.Token) error) error {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		t, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t.(type) {
		case xml.StartElement:
			depth++
			if depth > 128 {
				return errors.New("XML depth limit")
			}
		case xml.EndElement:
			depth--
		}
		if err := visit(t); err != nil {
			return err
		}
	}
}

func officeVersionParagraphs(data []byte) ([]string, error) {
	p, err := officeVersionPackageFor(data)
	if err != nil {
		return nil, err
	}
	body, err := p.part("word/document.xml")
	if err != nil {
		return nil, err
	}
	paragraphs := []string{}
	var value strings.Builder
	inParagraph, inText := false, false
	err = officeVersionXML(body, func(token xml.Token) error {
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				if inParagraph {
					return errors.New("nested paragraph")
				}
				inParagraph = true
				value.Reset()
			case "t":
				inText = inParagraph
			case "tab":
				if inParagraph {
					value.WriteByte('\t')
				}
			case "br", "cr":
				if inParagraph {
					value.WriteByte('\n')
				}
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
			if t.Name.Local == "p" && inParagraph {
				paragraphs = append(paragraphs, value.String())
				inParagraph = false
				if len(paragraphs) > officeVersionDiffParagraphs {
					return errors.New("paragraph limit")
				}
			}
		case xml.CharData:
			if inText {
				value.Write(t)
			}
		}
		return nil
	})
	return paragraphs, err
}

type officeVersionCell struct {
	Value   string
	Formula string
	Type    string
}

type officeVersionSheet struct {
	Name  string
	Cells map[string]officeVersionCell
}

func officeVersionAttr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func officeVersionWorkbook(data []byte) ([]officeVersionSheet, error) {
	p, err := officeVersionPackageFor(data)
	if err != nil {
		return nil, err
	}
	shared := []string{}
	if p.files["xl/sharedStrings.xml"] != nil {
		body, err := p.part("xl/sharedStrings.xml")
		if err != nil {
			return nil, err
		}
		var value strings.Builder
		inItem, inText := false, false
		if err = officeVersionXML(body, func(token xml.Token) error {
			switch t := token.(type) {
			case xml.StartElement:
				if t.Name.Local == "si" {
					inItem = true
					value.Reset()
				}
				if t.Name.Local == "t" && inItem {
					inText = true
				}
			case xml.EndElement:
				if t.Name.Local == "t" {
					inText = false
				}
				if t.Name.Local == "si" {
					shared = append(shared, value.String())
					inItem = false
					if len(shared) > officeVersionDiffCells {
						return errors.New("shared string limit")
					}
				}
			case xml.CharData:
				if inText {
					value.Write(t)
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	relBody, err := p.part("xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	rels := map[string]string{}
	if err = officeVersionXML(relBody, func(token xml.Token) error {
		if t, ok := token.(xml.StartElement); ok && t.Name.Local == "Relationship" && officeVersionAttr(t, "TargetMode") != "External" {
			target := officeVersionAttr(t, "Target")
			if strings.HasPrefix(target, "/") {
				target = strings.TrimPrefix(path.Clean(target), "/")
			} else {
				target = path.Clean(path.Join("xl", target))
			}
			if strings.HasPrefix(target, "xl/") && !strings.Contains(target, "\\") {
				rels[officeVersionAttr(t, "Id")] = target
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	workbook, err := p.part("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	var refs []struct{ name, part string }
	if err = officeVersionXML(workbook, func(token xml.Token) error {
		if t, ok := token.(xml.StartElement); ok && t.Name.Local == "sheet" {
			if len(refs) >= 1024 {
				return errors.New("sheet limit")
			}
			name := officeVersionAttr(t, "name")
			if name == "" || officeTextUnits(name) > 128 {
				return errors.New("worksheet name limit")
			}
			part := rels[officeVersionAttr(t, "id")]
			if part == "" {
				return errors.New("missing worksheet relationship")
			}
			refs = append(refs, struct{ name, part string }{name, part})
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sheets := []officeVersionSheet{}
	cellCount := 0
	for _, ref := range refs {
		body, err := p.part(ref.part)
		if err != nil {
			return nil, err
		}
		sheet := officeVersionSheet{Name: ref.name, Cells: map[string]officeVersionCell{}}
		var address, typ, field string
		var value, formula, inline strings.Builder
		inCell := false
		err = officeVersionXML(body, func(token xml.Token) error {
			switch t := token.(type) {
			case xml.StartElement:
				if t.Name.Local == "c" {
					if inCell {
						return errors.New("nested cell")
					}
					inCell = true
					address, typ = officeVersionAttr(t, "r"), officeVersionAttr(t, "t")
					if len(address) == 0 || len(address) > 32 || len(typ) > 16 {
						return errors.New("invalid cell address")
					}
					value.Reset()
					formula.Reset()
					inline.Reset()
				}
				if inCell && (t.Name.Local == "v" || t.Name.Local == "f" || t.Name.Local == "t") {
					field = t.Name.Local
				}
			case xml.EndElement:
				if t.Name.Local == field {
					field = ""
				}
				if t.Name.Local == "c" && inCell {
					v := value.String()
					if typ == "s" {
						i, err := strconv.Atoi(v)
						if err != nil || i < 0 || i >= len(shared) {
							return errors.New("invalid shared string")
						}
						v = shared[i]
					} else if typ == "inlineStr" {
						v = inline.String()
					}
					cell := officeVersionCell{Value: v, Formula: formula.String(), Type: typ}
					if v != "" || cell.Formula != "" {
						// Shared-string storage versus inline storage is not a user edit.
						if typ == "s" || typ == "inlineStr" || typ == "str" {
							cell.Type = "string"
						}
						sheet.Cells[address] = cell
					}
					cellCount++
					if cellCount > officeVersionDiffCells {
						return errors.New("cell comparison limit")
					}
					inCell = false
				}
			case xml.CharData:
				if inCell {
					switch field {
					case "v":
						value.Write(t)
					case "f":
						formula.Write(t)
					case "t":
						inline.Write(t)
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sheets = append(sheets, sheet)
	}
	return sheets, nil
}

func officeVersionAddChange(diff *officeVersionDiff, c officeVersionChange) {
	diff.ChangeCount++
	if len(diff.Changes) >= officeVersionDiffChanges {
		diff.Truncated = true
		return
	}
	var beforeCut, afterCut bool
	c.Before, beforeCut = officeActivityText(c.Before, 320)
	c.After, afterCut = officeActivityText(c.After, 320)
	diff.Truncated = diff.Truncated || beforeCut || afterCut
	diff.Changes = append(diff.Changes, c)
}

func officeVersionCellText(c officeVersionCell) string {
	if c.Formula != "" {
		return "=" + c.Formula + " [cached: " + c.Value + "]"
	}
	return c.Value
}

func officeVersionCompare(before, after []byte, fileType string) officeVersionDiff {
	diff := officeVersionDiff{Coverage: "unsupported", Changes: []officeVersionChange{}, Limitations: []string{}}
	unsupported := func() officeVersionDiff {
		diff.Coverage = "unsupported"
		diff.Truncated = true
		diff.ChangeCount = 0
		diff.Changes = []officeVersionChange{}
		diff.Limitations = []string{"结构对比未能在资源边界内完成；已保留完整文件版本，不代表没有修改。"}
		return diff
	}
	if fileType == "docx" {
		a, err := officeVersionParagraphs(before)
		b, second := officeVersionParagraphs(after)
		if err != nil || second != nil {
			return unsupported()
		}
		diff.Coverage = "paragraph-text"
		diff.Limitations = []string{"仅比较正文段落文字（包括表格内文字）；按段落位置对应，插入或删除会影响后续对应。", "不验证格式、表格结构、图片、修订、批注、页眉页脚或排版保真。"}
		for i := 0; i < len(a) || i < len(b); i++ {
			old, next := "", ""
			if i < len(a) {
				old = a[i]
			}
			if i < len(b) {
				next = b[i]
			}
			if old != next || (i >= len(a) || i >= len(b)) {
				officeVersionAddChange(&diff, officeVersionChange{Kind: "paragraph", Address: strconv.Itoa(i + 1), Before: old, After: next})
			}
		}
		return diff
	}
	if fileType != "xlsx" {
		return unsupported()
	}
	a, err := officeVersionWorkbook(before)
	b, second := officeVersionWorkbook(after)
	if err != nil || second != nil {
		return unsupported()
	}
	diff.Coverage = "cell-values-formulas"
	diff.Limitations = []string{"仅比较工作表名称及单元格存储值/公式；不重新计算公式，共享公式、缓存值与日期格式可能影响显示。", "不验证格式、样式、图表、图片、透视表、宏、外部链接或完整文档保真。"}
	oldSheets, newSheets := map[string]officeVersionSheet{}, map[string]officeVersionSheet{}
	names := map[string]bool{}
	for _, s := range a {
		oldSheets[s.Name], names[s.Name] = s, true
	}
	for _, s := range b {
		newSheets[s.Name], names[s.Name] = s, true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		old, was := oldSheets[name]
		next, exists := newSheets[name]
		if !was || !exists {
			previousName, nextName := "", ""
			if was {
				previousName = name
			}
			if exists {
				nextName = name
			}
			officeVersionAddChange(&diff, officeVersionChange{Kind: "sheet", Sheet: name, Before: previousName, After: nextName})
		}
		addresses := map[string]bool{}
		for address := range old.Cells {
			addresses[address] = true
		}
		for address := range next.Cells {
			addresses[address] = true
		}
		cells := make([]string, 0, len(addresses))
		for address := range addresses {
			cells = append(cells, address)
		}
		sort.Strings(cells)
		for _, address := range cells {
			previous, previousExists := old.Cells[address]
			current, currentExists := next.Cells[address]
			if previous != current || previousExists != currentExists {
				officeVersionAddChange(&diff, officeVersionChange{Kind: "cell", Sheet: name, Address: address, Before: officeVersionCellText(previous), After: officeVersionCellText(current)})
			}
		}
	}
	return diff
}
