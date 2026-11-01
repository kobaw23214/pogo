package web

// These tests are intentionally not executed as part of this source-only
// change. They exercise file revision contracts, not ONLYOFFICE engine fidelity.
import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func officeVersionTestPackage(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, text := range entries {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func officeVersionClosedCommit(t *testing.T) (*officeService, *officeLink, string, []byte, []byte, officeVersionRecord) {
	t.Helper()
	o, d, l, file := officeBatchFixture(t)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = o.closeDocument(d); err != nil {
		t.Fatal(err)
	}
	prepared, err := o.prepareBatch(context.Background(), l, file, "version_prepare_001", "batch", "")
	if err != nil {
		t.Fatal(err)
	}
	after := officeTestPackage(t, "script result")
	if err = os.WriteFile(prepared["staged_path"].(string), after, 0600); err != nil {
		t.Fatal(err)
	}
	if err = o.setBatchVersionMetadata(l, prepared["batch_id"].(string), officeVersionMetadata{
		Engine: "script", Actor: officeActivityAgent, ScriptDigest: officeHash([]byte("print('example')")), ExecutionID: "execution-001", Summary: "request intent",
	}); err != nil {
		t.Fatal(err)
	}
	committed, err := o.commitBatch(context.Background(), l, prepared["batch_id"].(string), "version_commit_001")
	if err != nil || committed["state"] != "committed" || committed["session"] != nil {
		t.Fatalf("offline commit must not open an editor: %#v %v", committed, err)
	}
	page, err := o.listVersions(context.Background(), l, file, "", 20)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("missing committed version: %#v %v", page, err)
	}
	return o, l, file, before, after, page.Items[0]
}

func TestOfficeVersionsSnapshotsAndStableFileIdentityAcrossConversations(t *testing.T) {
	o, l, file, before, after, version := officeVersionClosedCommit(t)
	if version.BeforeVersion != officeHash(before) || version.AfterVersion != officeHash(after) || version.Engine != "script" || version.ScriptDigest == "" || version.ExecutionID != "execution-001" {
		t.Fatalf("incorrect revision provenance: %#v", version)
	}
	other := o.link("conversation-2", l.cwd, l.owner, l.hub).(*officeLink)
	page, err := o.listVersions(context.Background(), other, file, "", 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != version.ID || page.Items[0].ConversationID != l.conversationID {
		t.Fatalf("same owned document lost its history in a new conversation: %#v %v", page, err)
	}
	scope, err := o.versionScope(other, file)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := o.versionRoot(scope)
	if err != nil {
		t.Fatal(err)
	}
	for hash, expected := range map[string][]byte{version.BeforeVersion: before, version.AfterVersion: after} {
		data, err := officeVersionRead(filepath.Join(dir, "blobs"), hash, officeMaxBytes)
		if err != nil || !bytes.Equal(data, expected) {
			t.Fatalf("full snapshot not retained: %s %v", hash, err)
		}
		info, err := os.Stat(filepath.Join(dir, "blobs", hash))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("snapshot permissions: %v %v", info, err)
		}
	}
}

func TestOfficeVersionsRestoreBeforeCreatesRevisionAndIsIdempotent(t *testing.T) {
	o, l, file, before, after, version := officeVersionClosedCommit(t)
	for i := 0; i < 2; i++ {
		reply, err := o.restoreVersionSide(context.Background(), l, file, version.ID, officeHash(after), "restore_before_001", "before", officeActivityUser)
		if err != nil || reply["state"] != "committed" || reply["session"] != nil {
			t.Fatalf("offline restore failed: %#v %v", reply, err)
		}
	}
	current, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(current, before) {
		t.Fatalf("restore did not publish full preimage: %v", err)
	}
	page, err := o.listVersions(context.Background(), l, file, "", 20)
	if err != nil || len(page.Items) != 2 || page.Items[0].Engine != "restore" || page.Items[0].Actor != officeActivityUser || page.Items[0].RestoredFrom != version.ID+":before" {
		t.Fatalf("restore not recorded as one new revision: %#v %v", page, err)
	}
	if _, err = o.restoreVersionSide(context.Background(), l, file, version.ID, officeHash(after), "restore_before_001", "after", officeActivityUser); err == nil {
		t.Fatal("same operation accepted a different restore side")
	}
}

func TestOfficeVersionsRestoreRejectsLaterFileChanges(t *testing.T) {
	o, l, file, _, after, version := officeVersionClosedCommit(t)
	human := officeTestPackage(t, "later human input")
	if err := os.WriteFile(file, human, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.restoreVersionSide(context.Background(), l, file, version.ID, officeHash(after), "restore_stale_001", "before", officeActivityAgent); err == nil {
		t.Fatal("stale expected_version overwrote a later edit")
	}
	current, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(current, human) {
		t.Fatalf("later input was lost: %v", err)
	}
}

func TestOfficeVersionDiffWordHasExplicitCoverage(t *testing.T) {
	makeDoc := func(text string) []byte {
		return officeVersionTestPackage(t, map[string]string{"[Content_Types].xml": "<Types/>", "word/document.xml": "<document><body><p><r><t>" + text + "</t></r></p><p><r><t>人工备注</t></r></p></body></document>"})
	}
	diff := officeVersionCompare(makeDoc("待评审"), makeDoc("评审中"), "docx")
	if diff.Coverage != "paragraph-text" || diff.ChangeCount != 1 || len(diff.Changes) != 1 || diff.Changes[0].Before != "待评审" || diff.Changes[0].After != "评审中" || len(diff.Limitations) == 0 {
		t.Fatalf("incorrect bounded Word comparison: %#v", diff)
	}
}

func TestOfficeVersionDiffXLSXNormalizesStringsAndObservesFormula(t *testing.T) {
	makeBook := func(shared bool, formula string) []byte {
		cell := `<c r="A1" t="inlineStr"><is><t>人工备注</t></is></c>`
		entries := map[string]string{
			"[Content_Types].xml":        "<Types/>",
			"xl/workbook.xml":            `<workbook xmlns:r="urn:relationships"><sheets><sheet name="数据" r:id="s1"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Target="worksheets/sheet1.xml"/></Relationships>`,
		}
		if shared {
			cell = `<c r="A1" t="s"><v>0</v></c>`
			entries["xl/sharedStrings.xml"] = `<sst><si><t>人工备注</t></si></sst>`
		}
		entries["xl/worksheets/sheet1.xml"] = `<worksheet><sheetData><row r="1">` + cell + `<c r="B1"><f>` + formula + `</f><v>2</v></c></row></sheetData></worksheet>`
		return officeVersionTestPackage(t, entries)
	}
	diff := officeVersionCompare(makeBook(true, "1+1"), makeBook(false, "SUM(1,1)"), "xlsx")
	if diff.Coverage != "cell-values-formulas" || diff.ChangeCount != 1 || diff.Changes[0].Address != "B1" || !strings.Contains(diff.Changes[0].After, "SUM(1,1)") || len(diff.Limitations) == 0 {
		t.Fatalf("incorrect bounded Excel comparison: %#v", diff)
	}
}
