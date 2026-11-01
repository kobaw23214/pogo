package web

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/skills"
)

// These checks exercise the public upload/preview/save/read/resolve path instead
// of synthesizing a store entry or reading the implementation's persistence file.
func TestOfficeInformationSnapshotSurvivesPrivateAndLibraryHTTPSave(t *testing.T) {
	for _, format := range []string{"word", "cell"} {
		t.Run(format, func(t *testing.T) {
			h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
			filename := "项目说明.docx"
			members := map[string]string{"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>原始文档已保存的选区</w:t></w:r></w:p></w:body></w:document>`}
			if format == "cell" {
				filename = "项目清单.xlsx"
				members = informationOfficeWorkbook(`<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>项目</t></is></c><c r="B1"><v>12</v></c></row></sheetData></worksheet>`)
			}
			blob := informationOfficeZIP(t, members)
			var uploaded informationResult
			if err := json.Unmarshal(infoUpload(t, h, aliceToken, "/api/information/files", filename, string(blob), map[string]string{"revision": "0", "parent_id": ""}, 201), &uploaded); err != nil {
				t.Fatal(err)
			}
			if uploaded.Entry == nil || uploaded.Entry.File == nil {
				t.Fatal("upload did not produce an actual file entry")
			}
			id := uploaded.Entry.ID
			var preview informationOfficePreview
			if err := json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+id+"/office-preview?version=1", nil, 200), &preview); err != nil {
				t.Fatal(err)
			}
			selection := &inputref.OfficeSnapshot{Path: preview.Path, Title: preview.Title, Editor: preview.Editor, CanvasVersion: "disk:" + preview.SHA256, CapturedAt: "2026-10-18T00:00:00Z", Information: &inputref.InformationOrigin{EntryID: preview.EntryID, Version: preview.Version, SnapshotSHA256: preview.SnapshotSHA256}}
			if format == "word" {
				selection.Text = preview.Paragraphs[0]
				selection.Locator = &inputref.OfficeLocator{Kind: "text", Exact: selection.Text}
			} else {
				selection.Text = preview.Sheets[0].Cells[0].Text + "\t" + preview.Sheets[0].Cells[1].Text
				selection.Sheet = preview.Sheets[0].Name
				selection.Range = "A1:B1"
			}
			if !skills.ValidOfficeSnapshot(selection) {
				t.Fatal("actual read-only preview could not become a valid selection")
			}
			parts := []inputref.Part{{Type: "text", Text: "先检查 "}, {Type: "office", Office: selection}, {Type: "text", Text: " 再处理 "}, {Type: "shortcut", ShortcutID: "nested", Title: "嵌套来源", Parts: []inputref.Part{{Type: "text", Text: "补充 "}, {Type: "office", Office: selection}}}, {Type: "text", Text: " 最后汇总"}}
			var initial shortcutView
			if err := json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/shortcuts", nil, 200), &initial); err != nil {
				t.Fatal(err)
			}
			private := userShortcut{ID: "ordered_saved_selection", Title: "保留来源的流程", Parts: parts, InputLayout: "inline"}
			infoJSON(t, h, aliceToken, "PUT", "/api/shortcuts", map[string]any{"scope_id": initial.ScopeID, "revision": initial.Revision, "shortcuts": []userShortcut{private}}, 200)
			var library informationResult
			if err := json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/information/entries", map[string]any{"revision": 1, "kind": "shortcut", "name": "资料选区流程", "shortcut": information.Shortcut{ID: "library_saved_selection", Title: "资料选区流程", Parts: parts, InputLayout: "inline"}}, 201), &library); err != nil {
				t.Fatal(err)
			}
			if library.Entry == nil {
				t.Fatal("library save did not create a shortcut")
			}
			// The file's public API has no replacement-bytes operation. A rename is
			// its supported v2 mutation; both saved references must still target v1.
			infoJSON(t, h, aliceToken, "PATCH", "/api/information/entries/"+id, map[string]any{"revision": 2, "name": "第二版_" + filename}, 200)
			var latest informationOfficePreview
			if err := json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+id+"/office-preview?version=2", nil, 200), &latest); err != nil {
				t.Fatal(err)
			}
			if latest.Version != 2 || latest.SnapshotSHA256 == preview.SnapshotSHA256 {
				t.Fatal("source v2 mutation was not real")
			}
			checkParts := func(got []inputref.Part) {
				t.Helper()
				if !reflect.DeepEqual(got, parts) {
					t.Fatalf("ordered saved snapshot changed: %+v", got)
				}
				for _, node := range inputref.Flatten(got) {
					if node.Office != nil {
						o := node.Office
						if o.Information == nil || o.Information.Version != 1 || o.Information.SnapshotSHA256 != preview.SnapshotSHA256 || o.SourceSession != "" || o.Dirty || !strings.Contains(skills.OfficeInputText(o), "保存版本 1；只读选取") {
							t.Fatal("saved provenance changed into a latest/live reference")
						}
					}
				}
			}
			h.mgr.evictAll()
			var savedPrivate shortcutView
			if err := json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/shortcuts", nil, 200), &savedPrivate); err != nil {
				t.Fatal(err)
			}
			if len(savedPrivate.Shortcuts) != 1 || savedPrivate.Shortcuts[0].InputLayout != "inline" {
				t.Fatal("private ordered shortcut did not survive persistence")
			}
			checkParts(savedPrivate.Shortcuts[0].Parts)
			var savedLibrary information.Entry
			if err := json.Unmarshal(infoJSON(t, h, aliceToken, "GET", "/api/information/entries/"+library.Entry.ID+"?version=1", nil, 200), &savedLibrary); err != nil {
				t.Fatal(err)
			}
			if savedLibrary.Shortcut == nil || savedLibrary.Shortcut.InputLayout != "inline" {
				t.Fatal("library ordered shortcut did not survive persistence")
			}
			checkParts(savedLibrary.Shortcut.Parts)
			var resolved struct {
				Parts []inputref.Part `json:"parts"`
			}
			sid := h.createAs(aliceToken)
			if err := json.Unmarshal(infoJSON(t, h, aliceToken, "POST", "/api/sessions/"+sid+"/information/resolve", map[string]any{"id": library.Entry.ID, "version": 1}, 200), &resolved); err != nil {
				t.Fatal(err)
			}
			if len(resolved.Parts) != 1 || resolved.Parts[0].Type != "shortcut" {
				t.Fatal("resolve lost the reusable shortcut atom")
			}
			checkParts(resolved.Parts[0].Parts)
			infoJSON(t, h, bobToken, "GET", "/api/information/entries/"+id+"/office-preview?version=1", nil, 404)
		})
	}
}

func TestOfficeInformationOriginCloneAndReadOnlyValidation(t *testing.T) {
	office := &inputref.OfficeSnapshot{Path: "information-assets/" + strings.Repeat("a", 64) + "/report.docx", Title: "Report", Editor: "word", Text: "exact selected content", CanvasVersion: "disk:" + strings.Repeat("a", 64), Locator: &inputref.OfficeLocator{Kind: "text", Exact: "exact selected content"}, Information: &inputref.InformationOrigin{EntryID: "inf_" + strings.Repeat("b", 32), Version: 1, SnapshotSHA256: strings.Repeat("c", 64)}}
	parts := []inputref.Part{{Type: "shortcut", ShortcutID: "outer", Title: "Outer", Parts: []inputref.Part{{Type: "office", Office: office}}}}
	cloned := inputref.Clone(parts)
	copyOffice := cloned[0].Parts[0].Office
	copyOffice.Information.Version = 2
	copyOffice.Locator.Exact = "modified clone"
	copyOffice.Text = "modified clone"
	if office.Information.Version != 1 || office.Locator.Exact != "exact selected content" || office.Text != "exact selected content" {
		t.Fatal("nested clone shares mutable Office/origin/locator pointers")
	}
	if !skills.ValidOfficeSnapshot(office) {
		t.Fatal("valid immutable read-only origin rejected")
	}
	for name, mutate := range map[string]func(*inputref.OfficeSnapshot){
		"dirty canvas":              func(o *inputref.OfficeSnapshot) { o.Dirty = true },
		"dirty epoch":               func(o *inputref.OfficeSnapshot) { o.DirtyEpoch = 1 },
		"live session":              func(o *inputref.OfficeSnapshot) { o.SourceSession = "foreign-session" },
		"mismatched disk hash":      func(o *inputref.OfficeSnapshot) { o.CanvasVersion = "disk:" + strings.Repeat("d", 64) },
		"live canvas version":       func(o *inputref.OfficeSnapshot) { o.CanvasVersion = "live-canvas-v1" },
		"workspace path":            func(o *inputref.OfficeSnapshot) { o.Path = "uploads/report.docx" },
		"bad entry":                 func(o *inputref.OfficeSnapshot) { o.Information.EntryID = "../../other-owner" },
		"latest instead of version": func(o *inputref.OfficeSnapshot) { o.Information.Version = 0 },
		"bad snapshot hash":         func(o *inputref.OfficeSnapshot) { o.Information.SnapshotSHA256 = "unsealed" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := inputref.Clone([]inputref.Part{{Type: "office", Office: office}})[0].Office
			mutate(copy)
			if skills.ValidOfficeSnapshot(copy) {
				t.Fatal("invalid information origin accepted")
			}
		})
	}
}
