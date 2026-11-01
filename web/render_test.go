package web

import (
	"archive/zip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zipFixture writes a zip with the given members (path → content) — the shape
// docx and xlsx both are.
func zipFixture(t *testing.T, abs string, members map[string]string) {
	t.Helper()
	f, err := os.Create(abs)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

const docxBody = `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>季度报告</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr/></w:pPr><w:r><w:t>第一项</w:t></w:r></w:p>
<w:p><w:r><w:t>普通段落</w:t></w:r></w:p>
<w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>主机</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>吞吐</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>7</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
</w:body></w:document>`

const xlsxWorkbook = `<?xml version="1.0"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>
<sheet name="Sheet1" sheetId="1" r:id="rId1"/><sheet name="数据" sheetId="2" r:id="rId2"/>
</sheets></workbook>`

const xlsxRels = `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
</Relationships>`

const xlsxShared = `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>主机</t></si><si><t><r><t>吞</t></r><r><t>吐</t></r></t></si></sst>`

const xlsxSheet1 = `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
<row r="2"><c r="A2"><v>1200</v></c><c r="B2" t="b"><v>1</v></c><c r="C2"><v></v></c></row>
</sheetData></worksheet>`

const xlsxSheet2 = `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>内联</t></is></c></row>
</sheetData></worksheet>`

func TestRenderDocxToMarkdown(t *testing.T) {
	h := filesHarness(t)
	zipFixture(t, filepath.Join(h.mgr.Cwd(), "报告.docx"), map[string]string{
		"word/document.xml": docxBody,
	})
	body := getJSON(t, h, "/api/files/render?path="+urlEncode("报告.docx"), http.StatusOK)
	text := body["text"].(string)
	for _, want := range []string{
		"# 季度报告",        // heading level off the style
		"- 第一项",         // numbered paragraph keeps its list marker
		"普通段落",          // plain paragraph
		"| 主机 | 吞吐 |",   // table header
		"| --- | --- |", // GFM separator
		"| B | 7 |",     // second row
	} {
		if !strings.Contains(text, want) {
			t.Errorf("docx markdown missing %q in:\n%s", want, text)
		}
	}
	if body["truncated"] == true {
		t.Error("small docx must not report truncation")
	}
}

func TestRenderXlsxToMarkdown(t *testing.T) {
	h := filesHarness(t)
	zipFixture(t, filepath.Join(h.mgr.Cwd(), "表.xlsx"), map[string]string{
		"xl/workbook.xml":            xlsxWorkbook,
		"xl/_rels/workbook.xml.rels": xlsxRels,
		"xl/sharedStrings.xml":       xlsxShared,
		"xl/worksheets/sheet1.xml":   xlsxSheet1,
		"xl/worksheets/sheet2.xml":   xlsxSheet2,
	})
	body := getJSON(t, h, "/api/files/render?path="+urlEncode("表.xlsx"), http.StatusOK)
	text := body["text"].(string)
	for _, want := range []string{
		"## Sheet1（2 行）",
		"| 主机 | 吞吐 |",     // shared strings resolved, rich runs concatenated
		"| 1200 | TRUE |", // numbers and booleans rendered, empty cell stays ""
		"## 数据（1 行）",
		"| 内联 |", // inline strings on the second sheet, rels order honoured
	} {
		if !strings.Contains(text, want) {
			t.Errorf("xlsx markdown missing %q in:\n%s", want, text)
		}
	}
}

func TestRenderRefusesNonConvertible(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	writeFixture(t, cwd, "旧格式.doc", "\xd0\xcf\x11\xe0binary")
	writeFixture(t, cwd, "notes.txt", "text")

	body := getJSON(t, h, "/api/files/render?path="+urlEncode("旧格式.doc"), http.StatusUnsupportedMediaType)
	if msg, _ := body["error"].(string); !strings.Contains(msg, "旧版") {
		t.Errorf(".doc error = %q, want the old-format explanation", msg)
	}
	body = getJSON(t, h, "/api/files/render?path=notes.txt", http.StatusUnsupportedMediaType)
	if msg, _ := body["error"].(string); !strings.Contains(msg, "无需服务端渲染") {
		t.Errorf(".txt error = %q, want the not-needed explanation", msg)
	}
}

func TestRenderRejectsBrokenZip(t *testing.T) {
	h := filesHarness(t)
	writeFixture(t, h.mgr.Cwd(), "fake.docx", "not a zip at all")
	getJSON(t, h, "/api/files/render?path=fake.docx", http.StatusUnsupportedMediaType)
}

func urlEncode(s string) string {
	// The harness queries go through url.Values-free string building; a tiny
	// escaper keeps the test readable for the CJK names above.
	var b strings.Builder
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c > 0x20 && c < 0x7f && c != '&' && c != '=' && c != '%' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}
	return b.String()
}

func TestFilesRawServesPdfInlineAndDownload(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	pdf := append([]byte("%PDF-1.4\n%垃圾\n1 0 obj\n"), make([]byte, 64)...)
	if err := os.WriteFile(filepath.Join(cwd, "报告.pdf"), pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	zipBytes := append([]byte("PK\x03\x04"), make([]byte, 64)...)
	if err := os.WriteFile(filepath.Join(cwd, "表.xlsx"), zipBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	resp := h.do(http.MethodGet, "/api/files/content?path="+urlEncode("报告.pdf")+"&raw=1", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw pdf: %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("pdf content-type = %s, want application/pdf", ct)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("pdf CSP = %q, want a sandbox policy", csp)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "inline") {
		t.Errorf("pdf disposition = %q, want inline", cd)
	}

	// download=1 serves any binary as an attachment — the only safe way to
	// hand a docx out of this origin.
	resp = h.do(http.MethodGet, "/api/files/content?path="+urlEncode("表.xlsx")+"&raw=1&download=1", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download xlsx: %d, want 200", resp.StatusCode)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") {
		t.Errorf("download disposition = %q, want attachment", cd)
	}
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("download disposition = %q, want an RFC 5987 filename for the CJK name", cd)
	}

	// Without download, inline raw stays closed to anything executable.
	resp = h.do(http.MethodGet, "/api/files/content?path="+urlEncode("表.xlsx")+"&raw=1", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("raw xlsx without download: %d, want 415", resp.StatusCode)
	}
}

func TestFilesRawServesAudioVideoInline(t *testing.T) {
	h := filesHarness(t)
	cases := []struct {
		name, mime string
		data       []byte
	}{
		{"语音.mp3", "audio/mpeg", append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 64)...)},
		{"演示.mp4", "video/mp4", append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 64)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(h.mgr.Cwd(), tc.name), tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			resp := h.do(http.MethodGet, "/api/files/content?path="+urlEncode(tc.name)+"&raw=1", "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("raw media: %d, want 200", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); got != tc.mime {
				t.Fatalf("media content-type = %q, want %q", got, tc.mime)
			}
			if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
				t.Fatalf("media CSP = %q, want sandbox", csp)
			}
			if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "inline") {
				t.Fatalf("media disposition = %q, want inline", cd)
			}
		})
	}
}

func TestFilesRawSupportsMediaRangeAndRejectsHTML(t *testing.T) {
	h := filesHarness(t)
	path := filepath.Join(h.mgr.Cwd(), "演示.mp4")
	data := append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 64)...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/files/content?path=演示.mp4&raw=1", nil)
	req.Header.Set("Range", "bytes=0-3")
	recorder := httptest.NewRecorder()
	serveRawFile(recorder, req, f, info)
	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", recorder.Code)
	}
	if got, want := recorder.Header().Get("Content-Range"), fmt.Sprintf("bytes 0-3/%d", len(data)); got != want {
		t.Fatalf("content-range = %q, want %q", got, want)
	}

	if err := os.WriteFile(filepath.Join(h.mgr.Cwd(), "fake.mp3"), []byte("<html><body>not audio</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := h.do(http.MethodGet, "/api/files/content?path=fake.mp3&raw=1", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("HTML disguised as media: %d, want 415", resp.StatusCode)
	}
}
