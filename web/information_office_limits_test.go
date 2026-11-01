package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestInformationOfficeResourceLimitsAreDistinctFromInvalidFormat(t *testing.T) {
	word := func(body string) map[string]string {
		return map[string]string{"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body + `</w:body></w:document>`}
	}
	archiveMembers := map[string]string{}
	for i := 0; i < 4097; i++ {
		archiveMembers[fmt.Sprintf("member-%d", i)] = ""
	}
	manySheets := informationOfficeWorkbook(`<worksheet/>`)
	manySheets["xl/workbook.xml"] = `<workbook xmlns:r="x"><sheets>` + strings.Repeat(`<sheet name="Sheet" r:id="rId1"/>`, 65) + `</sheets></workbook>`
	manyStrings := informationOfficeWorkbook(`<worksheet/>`)
	manyStrings["xl/sharedStrings.xml"] = `<sst>` + strings.Repeat(`<si><t/></si>`, informationOfficeCellLimit+1) + `</sst>`
	for name, members := range map[string]map[string]string{
		"XML byte budget":       {"word/document.xml": strings.Repeat(" ", informationOfficeXMLLimit+1)},
		"text budget":           word(`<w:p><w:r><w:t>` + strings.Repeat("a", informationOfficeTextLimit+1) + `</w:t></w:r></w:p>`),
		"paragraph budget":      word(strings.Repeat(`<w:p/>`, 20001)),
		"worksheet budget":      manySheets,
		"shared string budget":  manyStrings,
		"archive member budget": archiveMembers,
	} {
		t.Run(name, func(t *testing.T) {
			blob := informationOfficeZIP(t, members)
			extension := ".docx"
			if members["xl/workbook.xml"] != "" {
				extension = ".xlsx"
			}
			_, err := readInformationOffice(bytes.NewReader(blob), int64(len(blob)), "report"+extension)
			if !errors.Is(err, errInformationOfficePreviewBudget) {
				t.Fatalf("resource limit lost its classification: %v", err)
			}
		})
	}
	_, err := readInformationOffice(bytes.NewReader([]byte("not an Office ZIP")), 17, "report.docx")
	if err == nil || errors.Is(err, errInformationOfficePreviewBudget) {
		t.Fatalf("bad format was misclassified as a resource limit: %v", err)
	}
}

func TestInformationOfficeResourceHTTPErrorPreservesValidOriginal(t *testing.T) {
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	resourceFile := informationOfficeZIP(t, map[string]string{"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + strings.Repeat("a", informationOfficeTextLimit+1) + `</w:t></w:r></w:p></w:body></w:document>`})
	for index, scenario := range []struct {
		name   string
		data   []byte
		status int
		phrase string
	}{
		{"complex.docx", resourceFile, 413, "复杂度限制"},
		{"damaged.docx", []byte("not an Office archive"), 422, "格式完整"},
	} {
		var upload informationResult
		if err := json.Unmarshal(infoUpload(t, h, aliceToken, "/api/information/files", scenario.name, string(scenario.data), map[string]string{"revision": fmt.Sprint(index), "parent_id": ""}, 201), &upload); err != nil {
			t.Fatal(err)
		}
		if upload.Entry == nil {
			t.Fatal("original upload missing")
		}
		target := "/api/information/entries/" + upload.Entry.ID
		body := infoJSON(t, h, aliceToken, "GET", target+"/office-preview?version=1", nil, scenario.status)
		if !bytes.Contains(body, []byte(scenario.phrase)) {
			t.Fatalf("wrong actionable preview error: %s", body)
		}
		if scenario.status == 413 && (bytes.Contains(body, []byte("格式完整")) || bytes.Contains(body, []byte("未加密")) || !bytes.Contains(body, []byte("原文件仍保留"))) {
			t.Fatalf("resource limit is presented as corruption or file loss: %s", body)
		}
		infoJSON(t, h, aliceToken, "GET", target+"?version=1", nil, 200)
		response := h.doAs(aliceToken, "GET", target+"/content?version=1&download=1", "")
		original, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Equal(original, scenario.data) {
			t.Fatal("preview failure damaged or removed the downloadable original", err)
		}
	}
}
