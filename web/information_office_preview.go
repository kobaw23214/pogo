package web

// This endpoint reads immutable library assets. It never opens an Office editing
// session, evaluates a formula, follows an external relationship, or writes a file.
import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/yosukeno/pogo/information"
)

const informationOfficeCompressedLimit = 32 << 20
const informationOfficeXMLLimit = 32 << 20
const informationOfficeTextLimit = 4 << 20
const informationOfficeCellLimit = 100000

// Resource limits are not evidence that an otherwise valid Office file is
// damaged or encrypted. Keep this classification through all parser helpers.
var errInformationOfficePreviewBudget = errors.New("Office preview resource limit exceeded")

type informationOfficeCell struct {
	Row  int    `json:"row"`
	Col  int    `json:"col"`
	Text string `json:"text"`
}
type informationOfficeSheet struct {
	Name    string                  `json:"name"`
	Rows    int                     `json:"rows"`
	Columns int                     `json:"columns"`
	Cells   []informationOfficeCell `json:"cells"`
}
type informationOfficePreview struct {
	EntryID        string                   `json:"entry_id"`
	Version        int64                    `json:"version"`
	SHA256         string                   `json:"sha256"`
	SnapshotSHA256 string                   `json:"snapshot_sha256"`
	Path           string                   `json:"path"`
	Title          string                   `json:"title"`
	Editor         string                   `json:"editor"`
	Paragraphs     []string                 `json:"paragraphs,omitempty"`
	Sheets         []informationOfficeSheet `json:"sheets,omitempty"`
	Truncated      bool                     `json:"truncated"`
}

func (s *Server) handleInformationOfficePreview(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	version, err := informationVersion(r)
	if err != nil || version == 0 {
		informationError(w, store, information.ErrInvalid)
		return
	}
	entry, err := store.Get(r.PathValue("id"), version)
	if err != nil {
		informationError(w, store, err)
		return
	}
	if entry.File == nil || (strings.ToLower(path.Ext(entry.File.Name)) != ".docx" && strings.ToLower(path.Ext(entry.File.Name)) != ".xlsx") {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("请选择信息库中的 .docx 或 .xlsx 文件"))
		return
	}
	if entry.File.Size > informationOfficeCompressedLimit {
		writeErr(w, 413, errors.New("只读选区预览最多支持 32 MiB 的 Office 文件，原文件仍保留在信息库中"))
		return
	}
	f, asset, err := store.AssetRead(entry.File.Asset)
	if err != nil {
		informationError(w, store, err)
		return
	}
	defer f.Close()
	if asset.SHA256 != entry.File.SHA256 || asset.Size != entry.File.Size {
		informationError(w, store, information.ErrInvalid)
		return
	}
	preview, err := readInformationOffice(f, asset.Size, entry.File.Name)
	if err != nil {
		if errors.Is(err, errInformationOfficePreviewBudget) {
			writeErr(w, 413, errors.New("文件内容超出只读选区预览的复杂度限制，暂时无法制作选区。原文件仍保留在信息库，可下载或整份引用；也可以上传范围更小的文档或工作表副本"))
			return
		}
		writeErr(w, 422, errors.New("无法读取这份 Office 文件的保存内容，请确认文件未加密且格式完整"))
		return
	}
	preview.EntryID, preview.Version, preview.SHA256, preview.Path, preview.Title = entry.ID, entry.Version, asset.SHA256, asset.Asset, entry.Name
	sealed, err := json.Marshal(entry)
	if err != nil {
		informationError(w, store, information.ErrInvalid)
		return
	}
	hash := sha256.Sum256(sealed)
	preview.SnapshotSHA256 = hex.EncodeToString(hash[:])
	writeJSON(w, 200, preview)
}

type informationOfficeArchive struct {
	files     map[string]*zip.File
	remaining int64
	textBytes int
	cells     int
}

func (a *informationOfficeArchive) xml(name string) (*xml.Decoder, io.Closer, error) {
	f := a.files[name]
	if f == nil {
		return nil, nil, fmt.Errorf("missing Office member")
	}
	if f.UncompressedSize64 > uint64(a.remaining) {
		return nil, nil, fmt.Errorf("%w: XML bytes", errInformationOfficePreviewBudget)
	}
	a.remaining -= int64(f.UncompressedSize64)
	r, err := f.Open()
	if err != nil {
		return nil, nil, err
	}
	// zip.File enforces the advertised length and CRC. The extra byte prevents
	// a malformed member from turning its declared length into an unbounded read.
	return xml.NewDecoder(io.LimitReader(r, int64(f.UncompressedSize64)+1)), r, nil
}
func (a *informationOfficeArchive) addText(text string) error {
	a.textBytes += len(text)
	if a.textBytes > informationOfficeTextLimit {
		return fmt.Errorf("%w: text bytes", errInformationOfficePreviewBudget)
	}
	return nil
}
func readInformationOffice(reader io.ReaderAt, size int64, name string) (informationOfficePreview, error) {
	var result informationOfficePreview
	if size < 1 {
		return result, errors.New("empty Office archive")
	}
	if size > informationOfficeCompressedLimit {
		return result, fmt.Errorf("%w: compressed bytes", errInformationOfficePreviewBudget)
	}
	z, err := zip.NewReader(reader, size)
	if err != nil {
		return result, err
	}
	if len(z.File) > 4096 {
		return result, fmt.Errorf("%w: archive member count", errInformationOfficePreviewBudget)
	}
	a := informationOfficeArchive{files: map[string]*zip.File{}, remaining: informationOfficeXMLLimit}
	for _, f := range z.File {
		if f.Name == "" || strings.ContainsAny(f.Name, "\\\x00") || strings.HasPrefix(f.Name, "/") || path.Clean(f.Name) != strings.TrimSuffix(f.Name, "/") || strings.HasPrefix(f.Name, "../") {
			return result, errors.New("invalid archive member")
		}
		if _, duplicate := a.files[f.Name]; duplicate {
			return result, errors.New("duplicate archive member")
		}
		a.files[f.Name] = f
	}
	if strings.EqualFold(path.Ext(name), ".docx") {
		result.Editor = "word"
		result.Paragraphs, err = a.word()
	} else {
		result.Editor = "cell"
		result.Sheets, result.Truncated, err = a.workbook()
	}
	return result, err
}
func (a *informationOfficeArchive) word() ([]string, error) {
	d, r, err := a.xml("word/document.xml")
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var paragraphs []string
	var paragraph strings.Builder
	inBody, inParagraph, inText := false, false, false
	for {
		token, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Space != "http://schemas.openxmlformats.org/wordprocessingml/2006/main" && value.Name.Space != "http://purl.oclc.org/ooxml/wordprocessingml/main" {
				continue
			}
			switch value.Name.Local {
			case "body":
				inBody = true
			case "p":
				if inBody {
					inParagraph = true
					paragraph.Reset()
				}
			case "t":
				inText = inParagraph
			case "tab":
				if inParagraph {
					paragraph.WriteByte('\t')
				}
			case "br", "cr":
				if inParagraph {
					paragraph.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inText {
				if e = a.addText(string(value)); e != nil {
					return nil, e
				}
				paragraph.Write(value)
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "p":
				if inParagraph {
					paragraphs = append(paragraphs, paragraph.String())
					if len(paragraphs) > 20000 {
						return nil, fmt.Errorf("%w: paragraph count", errInformationOfficePreviewBudget)
					}
					inParagraph = false
				}
			case "body":
				inBody = false
			}
		}
	}
	if len(paragraphs) == 0 {
		return nil, errors.New("empty Word body")
	}
	return paragraphs, nil
}

type informationWorkbookSheet struct {
	Name string `xml:"name,attr"`
	ID   string `xml:"id,attr"`
}
type informationWorkbook struct {
	Sheets []informationWorkbookSheet `xml:"sheets>sheet"`
}
type informationRelationship struct {
	ID     string `xml:"Id,attr"`
	Target string `xml:"Target,attr"`
	Mode   string `xml:"TargetMode,attr"`
	Type   string `xml:"Type,attr"`
}

func (a *informationOfficeArchive) decode(name string, target any) error {
	d, r, err := a.xml(name)
	if err != nil {
		return err
	}
	defer r.Close()
	return d.Decode(target)
}
func (a *informationOfficeArchive) workbook() ([]informationOfficeSheet, bool, error) {
	var book informationWorkbook
	if err := a.decode("xl/workbook.xml", &book); err != nil {
		return nil, false, err
	}
	if len(book.Sheets) == 0 {
		return nil, false, errors.New("unsupported sheet count")
	}
	if len(book.Sheets) > 64 {
		return nil, false, fmt.Errorf("%w: worksheet count", errInformationOfficePreviewBudget)
	}
	var rels struct {
		Relations []informationRelationship `xml:"Relationship"`
	}
	if err := a.decode("xl/_rels/workbook.xml.rels", &rels); err != nil {
		return nil, false, err
	}
	targets := map[string]string{}
	for _, rel := range rels.Relations {
		if rel.Mode != "" || !strings.HasSuffix(rel.Type, "/worksheet") {
			continue
		}
		target := strings.TrimPrefix(rel.Target, "/")
		if !strings.HasPrefix(rel.Target, "/") {
			target = path.Join("xl", target)
		}
		if !strings.HasPrefix(target, "xl/worksheets/") || strings.ContainsAny(target, "\\\x00?#") || path.Clean(target) != target {
			continue
		}
		targets[rel.ID] = target
	}
	shared, err := a.sharedStrings()
	if err != nil {
		return nil, false, err
	}
	var sheets []informationOfficeSheet
	truncated := false
	for _, sheet := range book.Sheets {
		target, ok := targets[sheet.ID]
		if !ok {
			return nil, false, errors.New("unavailable worksheet relationship")
		}
		value, cut, e := a.sheet(target, sheet.Name, shared)
		if e != nil {
			return nil, false, e
		}
		sheets = append(sheets, value)
		truncated = truncated || cut
	}
	return sheets, truncated, nil
}
func (a *informationOfficeArchive) sharedStrings() ([]string, error) {
	if a.files["xl/sharedStrings.xml"] == nil {
		return nil, nil
	}
	d, r, err := a.xml("xl/sharedStrings.xml")
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var result []string
	var text strings.Builder
	inside, reading := false, false
	for {
		token, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local == "si" {
				inside = true
				text.Reset()
			}
			if value.Name.Local == "t" && inside {
				reading = true
			}
		case xml.CharData:
			if reading {
				if e = a.addText(string(value)); e != nil {
					return nil, e
				}
				text.Write(value)
			}
		case xml.EndElement:
			if value.Name.Local == "t" {
				reading = false
			}
			if value.Name.Local == "si" {
				result = append(result, text.String())
				inside = false
				if len(result) > informationOfficeCellLimit {
					return nil, fmt.Errorf("%w: shared string count", errInformationOfficePreviewBudget)
				}
			}
		}
	}
	return result, nil
}
func informationCellAddress(value string) (int, int, bool) {
	index, col := 0, 0
	for index < len(value) && value[index] >= 'A' && value[index] <= 'Z' {
		col = col*26 + int(value[index]-'A'+1)
		index++
		if col > 16384 {
			return 0, 0, false
		}
	}
	if index == 0 || index == len(value) {
		return 0, 0, false
	}
	row, err := strconv.Atoi(value[index:])
	return row, col, err == nil && row > 0 && row <= 1048576
}
func (a *informationOfficeArchive) sheet(name, title string, shared []string) (informationOfficeSheet, bool, error) {
	result := informationOfficeSheet{Name: title, Rows: 1, Columns: 1, Cells: []informationOfficeCell{}}
	d, r, err := a.xml(name)
	if err != nil {
		return result, false, err
	}
	defer r.Close()
	cut := false
	for {
		token, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return result, cut, e
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "c" {
			continue
		}
		var cell struct {
			Address string `xml:"r,attr"`
			Type    string `xml:"t,attr"`
			Value   string `xml:"v"`
			Formula string `xml:"f"`
			Inline  struct {
				Text string `xml:"t"`
				Runs []struct {
					Text string `xml:"t"`
				} `xml:"r"`
			} `xml:"is"`
		}
		if err = d.DecodeElement(&cell, &start); err != nil {
			return result, cut, err
		}
		row, col, valid := informationCellAddress(cell.Address)
		if !valid {
			return result, cut, errors.New("invalid cell address")
		}
		if row > 10000 || col > 256 || a.cells >= informationOfficeCellLimit {
			cut = true
			continue
		}
		text := cell.Value
		if cell.Type == "s" {
			i, e := strconv.Atoi(text)
			if e != nil || i < 0 || i >= len(shared) {
				return result, cut, errors.New("invalid shared string")
			}
			text = shared[i]
		}
		if cell.Type == "inlineStr" {
			text = cell.Inline.Text
			for _, run := range cell.Inline.Runs {
				text += run.Text
			}
		}
		if cell.Type == "b" {
			if text == "1" {
				text = "TRUE"
			} else {
				text = "FALSE"
			}
		}
		if text == "" && cell.Formula != "" {
			text = "=" + cell.Formula
		}
		if err = a.addText(text); err != nil {
			return result, cut, err
		}
		if text != "" {
			result.Cells = append(result.Cells, informationOfficeCell{Row: row, Col: col, Text: text})
			a.cells++
		}
		if row > result.Rows {
			result.Rows = row
		}
		if col > result.Columns {
			result.Columns = col
		}
	}
	return result, cut, nil
}
