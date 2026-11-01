package web

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/information"
)

func informationOfficeZIP(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	for name, text := range members {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func informationOfficeWorkbook(sheet string) map[string]string {
	return map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="明细" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   sheet,
	}
}
func TestInformationOfficePreviewWordSafeTextAndExcelCachedSelection(t *testing.T) {
	word := informationOfficeZIP(t, map[string]string{"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>&lt;script&gt;not executable&lt;/script&gt;</w:t></w:r></w:p><w:p><w:r><w:t>中文</w:t><w:tab/><w:t>内容</w:t><w:br/><w:t>下一行</w:t></w:r></w:p></w:body></w:document>`})
	p, err := readInformationOffice(bytes.NewReader(word), int64(len(word)), "Report.DOCX")
	if err != nil || p.Editor != "word" || len(p.Paragraphs) != 2 || p.Paragraphs[0] != "<script>not executable</script>" || p.Paragraphs[1] != "中文\t内容\n下一行" {
		t.Fatalf("word preview %#v err=%v", p, err)
	}
	members := informationOfficeWorkbook(`<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"><is><r><t>数</t></r><r><t>量</t></r></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>中国</t></is></c><c r="B2"><f>SUM(1,2)</f><v>3</v></c><c r="C2"><f>HYPERLINK("https://example.invalid")</f></c><c r="D2" t="b"><v>1</v></c></row></sheetData></worksheet>`)
	members["xl/sharedStrings.xml"] = `<sst><si><r><t>国</t></r><r><t>家</t></r></si></sst>`
	xlsx := informationOfficeZIP(t, members)
	p, err = readInformationOffice(bytes.NewReader(xlsx), int64(len(xlsx)), "data.xlsx")
	if err != nil || p.Editor != "cell" || len(p.Sheets) != 1 || p.Sheets[0].Name != "明细" || p.Sheets[0].Rows != 2 || p.Sheets[0].Columns != 4 {
		t.Fatalf("sheet preview %#v err=%v", p, err)
	}
	cells := p.Sheets[0].Cells
	if len(cells) != 6 || cells[0].Text != "国家" || cells[1].Text != "数量" || cells[3].Text != "3" || cells[4].Text != `=HYPERLINK("https://example.invalid")` || cells[5].Text != "TRUE" {
		t.Fatalf("cached/plain cells=%#v", cells)
	}
}
func TestInformationOfficePreviewRejectsUnsafeOrUnsupportedArchives(t *testing.T) {
	for name, members := range map[string]map[string]string{
		"external relationship": {"xl/workbook.xml": `<workbook xmlns:r="x"><sheets><sheet name="外部" r:id="rId1"/></sheets></workbook>`, "xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="http://example.invalid/data" TargetMode="External" Type="x/worksheet"/></Relationships>`},
		"path traversal":        {"../word/document.xml": "bad"},
		"entity":                {"word/document.xml": `<!DOCTYPE x [<!ENTITY unsafe SYSTEM "file:///etc/passwd">]><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:t>&unsafe;</w:t></w:p></w:body></w:document>`},
		"bad cell":              informationOfficeWorkbook(`<worksheet><c r="A0"><v>1</v></c></worksheet>`),
		"bad shared":            informationOfficeWorkbook(`<worksheet><c r="A1" t="s"><v>999</v></c></worksheet>`),
		"too much XML":          {"word/document.xml": strings.Repeat(" ", informationOfficeXMLLimit+1)},
	} {
		t.Run(name, func(t *testing.T) {
			blob := informationOfficeZIP(t, members)
			ext := ".docx"
			if members["xl/workbook.xml"] != "" {
				ext = ".xlsx"
			}
			if _, err := readInformationOffice(bytes.NewReader(blob), int64(len(blob)), "test"+ext); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}
func TestInformationOfficePreviewTruncationIsExplicit(t *testing.T) {
	members := informationOfficeWorkbook(`<worksheet><c r="A1" t="inlineStr"><is><t>visible</t></is></c><c r="A10001"><v>hidden row</v></c><c r="IW1"><v>hidden column</v></c></worksheet>`)
	blob := informationOfficeZIP(t, members)
	p, err := readInformationOffice(bytes.NewReader(blob), int64(len(blob)), "test.xlsx")
	if err != nil || !p.Truncated || p.Sheets[0].Rows != 1 || p.Sheets[0].Columns != 1 || len(p.Sheets[0].Cells) != 1 {
		t.Fatalf("truncation=%#v %v", p, err)
	}
}
func TestInformationOfficeAPIRequiresOwnedImmutableFileAndNeverOpensEditor(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	word := informationOfficeZIP(t, map[string]string{"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Saved immutable text</w:t></w:r></w:p></w:body></w:document>`})
	var upload informationResult
	if err := json.Unmarshal(infoUpload(t, h, aliceToken, "/api/information/files", "only-saved.docx", string(word), map[string]string{"revision": "0", "parent_id": ""}, 201), &upload); err != nil {
		t.Fatal(err)
	}
	id := upload.Entry.ID
	target := "/api/information/entries/" + id + "/office-preview?version=1"
	infoJSON(t, h, "", "GET", target, nil, 401)
	infoJSON(t, h, bobToken, "GET", target, nil, 404)
	infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+id+"/office-preview", nil, 400)
	infoJSON(t, h, aliceToken, "GET", target+"&version=1", nil, 400)
	var preview informationOfficePreview
	if err := json.Unmarshal(infoJSON(t, h, aliceToken, "GET", target, nil, 200), &preview); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(upload.Entry)
	sum := sha256.Sum256(raw)
	if preview.Path != upload.Entry.File.Asset || preview.SHA256 != upload.Entry.File.SHA256 || preview.SnapshotSHA256 != hex.EncodeToString(sum[:]) || preview.Paragraphs[0] != "Saved immutable text" {
		t.Fatalf("provenance not sealed: %#v", preview)
	}
	infoJSON(t, h, aliceToken, "PATCH", "/api/information/entries/"+id, map[string]any{"revision": 1, "name": "renamed.docx"}, 200)
	var old informationOfficePreview
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "GET", target, nil, 200), &old)
	if old.Title != "only-saved.docx" || old.Version != 1 || old.SnapshotSHA256 != preview.SnapshotSHA256 {
		t.Fatal("old snapshot changed after rename")
	}
	infoJSON(t, h, aliceToken, "POST", "/api/information/entries", map[string]any{"revision": 2, "kind": "shortcut", "name": "Not Office", "shortcut": information.Shortcut{ID: "ordinary", Title: "Not Office", Content: "body"}}, 201)
	var tree information.TreeView
	_ = json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/information", nil, 200), &tree)
	for _, entry := range tree.Entries {
		if entry.Kind != "file" {
			infoJSON(t, h, aliceToken, "GET", fmt.Sprintf("/api/information/entries/%s/office-preview?version=%d", entry.ID, entry.Version), nil, 415)
		}
	}
}
