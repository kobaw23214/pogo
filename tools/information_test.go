package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
)

func informationFixture(t *testing.T) (*information.Store, string) {
	t.Helper()
	store, err := information.Open(filepath.Join(t.TempDir(), "library"), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return store, t.TempDir()
}

func createInformation(t *testing.T, store *information.Store, in information.CreateInput) information.Entry {
	t.Helper()
	view, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	_, entry, err := store.Create(view.Revision, in)
	if err != nil {
		t.Fatal(err)
	}
	return *entry
}

func callInformation(t *testing.T, tool *Information, args any) Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInformationMetadataSearchAndImmutableRead(t *testing.T) {
	store, cwd := informationFixture(t)
	folder := createInformation(t, store, information.CreateInput{Kind: "folder", Name: "业务资料"})
	entry := createInformation(t, store, information.CreateInput{Kind: "shortcut", Name: "SQL资料", ParentID: folder.ID,
		Shortcut: &information.Shortcut{ID: "sql", Title: "SQL资料", Content: "database password example-only; 查询字段"}})
	tool := &Information{Store: store, Cwd: cwd}
	listing := callInformation(t, tool, informationArgs{Action: "list", ParentID: folder.ID})
	if strings.Contains(listing.Text, "password") || !strings.Contains(listing.Text, entry.ID) {
		t.Fatalf("metadata leaked body or lost child: %s", listing.Text)
	}
	search := callInformation(t, tool, informationArgs{Action: "search", Query: "查询字段"})
	if !strings.Contains(search.Text, entry.ID) || !strings.Contains(search.Text, "查询字段") {
		t.Fatal(search.Text)
	}
	read := callInformation(t, tool, informationArgs{Action: "read", ID: entry.ID})
	stamp := read.Details.(InformationDetails).Information
	if stamp.EntryID != entry.ID || stamp.Version != 1 || len(stamp.SnapshotSHA256) != 64 || !strings.Contains(read.Text, "example-only") {
		t.Fatalf("invalid read stamp: %+v %s", stamp, read.Text)
	}
	view, _ := store.List()
	_, _, err := store.Update(view.Revision, entry.ID, information.UpdateInput{Shortcut: &information.Shortcut{ID: "sql", Title: "SQL资料", Content: "updated"}})
	if err != nil {
		t.Fatal(err)
	}
	old := callInformation(t, tool, informationArgs{Action: "read", ID: entry.ID, Version: 1})
	if old.Details.(InformationDetails).Information.SnapshotSHA256 != stamp.SnapshotSHA256 || !strings.Contains(old.Text, "example-only") {
		t.Fatal("old snapshot changed")
	}
	view, _ = store.List()
	if _, _, err = store.Delete(view.Revision, entry.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = tool.Execute(context.Background(), json.RawMessage(`{"action":"read","id":"`+entry.ID+`"}`)); err == nil {
		t.Fatal("deleted current entry still readable")
	}
	callInformation(t, tool, informationArgs{Action: "read", ID: entry.ID, Version: 1})
	other, err := information.Open(filepath.Join(t.TempDir(), "other"), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&Information{Store: other, Cwd: cwd}).Execute(context.Background(), json.RawMessage(`{"action":"read","id":"`+entry.ID+`","version":1}`)); err == nil {
		t.Fatal("entry leaked to another owner's store")
	}
}

func TestInformationNestedAssetsAreVerifiedAndOfficeIsSnapshotOnly(t *testing.T) {
	store, cwd := informationFixture(t)
	asset, err := store.PutAsset("sample.csv", "", strings.NewReader("name,value\na,1\n"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	parts := []inputref.Part{
		{Type: "text", Text: "first "},
		{Type: "shortcut", ShortcutID: "nested", Title: "nested", Text: "nested", Parts: []inputref.Part{
			{Type: "file", Path: asset.Asset},
			{Type: "skill", Name: "office"},
			{Type: "office", Office: &inputref.OfficeSnapshot{Path: "uploads/original.xlsx", Title: "未保存表格", Editor: "cell", Text: "A1 snapshot value", Sheet: "Sheet1", Range: "A1", CanvasVersion: "v1", SourceSession: "other-owned-session"}},
		}},
		{Type: "text", Text: " last"},
	}
	entry := createInformation(t, store, information.CreateInput{Kind: "shortcut", Name: "混合资料", Shortcut: &information.Shortcut{ID: "mixed", Title: "混合资料", Content: "frozen fallback", Parts: parts, InputLayout: "inline"}})
	tool := &Information{Store: store, Cwd: cwd}
	result := callInformation(t, tool, informationArgs{Action: "read", ID: entry.ID})
	details := result.Details.(InformationDetails)
	if len(details.Files) != 1 || !strings.Contains(result.Text, "A1 snapshot value") || !strings.Contains(result.Text, "未执行") || strings.Contains(result.Text, "uploads/original.xlsx") || strings.Contains(result.Text, "other-owned-session") {
		t.Fatalf("nested references lost/broadened: %s %+v", result.Text, details)
	}
	copy, err := os.ReadFile(filepath.Join(cwd, filepath.FromSlash(details.Files[0].Path)))
	if err != nil || string(copy) != "name,value\na,1\n" {
		t.Fatalf("asset export: %q %v", copy, err)
	}
	if strings.Index(result.Text, "first") > strings.Index(result.Text, "nested") || strings.Index(result.Text, "last") < strings.Index(result.Text, "A1 snapshot") {
		t.Fatal("ordered material was reordered")
	}
	// The library is not a generic read/write grant for either registry mode.
	for _, readOnly := range []bool{true, false} {
		registry := New(Options{Cwd: cwd, Information: tool, ReadOnly: readOnly})
		if _, ok := registry.Get(InformationToolName); !ok {
			t.Fatal("child mode lost information tool")
		}
		if !readOnly {
			writer, _ := registry.Get("write")
			raw, _ := json.Marshal(map[string]any{"path": filepath.Join(store.Root(), "forged.txt"), "content": "bad"})
			if _, err := writer.Execute(context.Background(), raw); err == nil {
				t.Fatal("information root became writable")
			}
		}
	}
	if err := os.WriteFile(filepath.Join(store.Root(), "assets", asset.SHA256, "data"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"read","id":"`+entry.ID+`"}`)); err == nil {
		t.Fatal("tampered library bytes were exported")
	}
}

func TestInformationUTF8PagingAndBinaryHonesty(t *testing.T) {
	store, cwd := informationFixture(t)
	asset, err := store.PutAsset("binary.xlsx", "", strings.NewReader("PK\x00binary workbook"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	file := createInformation(t, store, information.CreateInput{Kind: "file", Name: "workbook", File: &asset})
	tool := &Information{Store: store, Cwd: cwd}
	result := callInformation(t, tool, informationArgs{Action: "read", ID: file.ID})
	if !strings.Contains(result.Text, "without extracting") || strings.Contains(result.Text, "binary workbook") {
		t.Fatal("claimed extraction of binary: " + result.Text)
	}
	entry := createInformation(t, store, information.CreateInput{Kind: "shortcut", Name: "unicode", Shortcut: &information.Shortcut{ID: "unicode", Title: "unicode", Content: "甲😀乙\n丙"}})
	result = callInformation(t, tool, informationArgs{Action: "read", ID: entry.ID, Offset: 1, Limit: 2})
	var out struct {
		Content string `json:"content"`
		Next    int    `json:"next_offset"`
	}
	if err = json.Unmarshal([]byte(result.Text), &out); err != nil || out.Content != "😀乙" || out.Next != 3 {
		t.Fatalf("bad Unicode page: %s %v", result.Text, err)
	}
	for _, raw := range []string{`{"action":"write"}`, `{"action":"read","id":"../../other"}`, `{"action":"read","id":"` + entry.ID + `","root":"/tmp"}`, `{"action":"search","query":"x","offset":101}`, `{"action":"read","id":"` + entry.ID + `","limit":8001}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted unsafe request %s", raw)
		}
	}
}

func TestInformationEnvironmentAndRetainedPermissionAreScoped(t *testing.T) {
	t.Setenv(EnvInformationRoot, "/unrelated/global-owner")
	t.Setenv(EnvInformationScope, strings.Repeat("f", 64))
	env := informationChildEnv(os.Environ(), "/trusted/owner", strings.Repeat("a", 64))
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "global-owner") || strings.Count(joined, EnvInformationRoot+"=") != 1 || !strings.Contains(joined, "/trusted/owner") {
		t.Fatal("wrong child scope")
	}
	if env = informationChildEnv(env, "", ""); strings.Contains(strings.Join(env, "\n"), "POGO_INFORMATION_") {
		t.Fatal("ungranted child inherited library")
	}
	s := &Subagent{Cwd: "/work", InformationRoot: "/trusted/a", InformationScope: strings.Repeat("a", 64)}
	key := s.permissionKey(ModeExplore)
	s.InformationRoot = "/trusted/b"
	if key == s.permissionKey(ModeExplore) {
		t.Fatal("retained context survived a root scope change")
	}
	s.InformationRoot = "/trusted/a"
	s.InformationScope = strings.Repeat("b", 64)
	if key == s.permissionKey(ModeExplore) {
		t.Fatal("retained context survived an owner scope change")
	}
	// A CLI with no information grant keeps its existing retained boundary key.
	plain := &Subagent{Cwd: "/work"}
	legacy := struct {
		Cwd, Mode, ReadRoots          string
		Skills, SkillRoots, ReadTools []string
		Explicit, Browser, Gate       bool
	}{plain.Cwd, ModeExplore, os.Getenv(EnvReadRoots), plain.SkillPaths, plain.SkillReadRoots, plain.ReadOnlySkillTools, false, false, false}
	raw, _ := json.Marshal(legacy)
	sum := sha256.Sum256(raw)
	if plain.permissionKey(ModeExplore) != hex.EncodeToString(sum[:]) {
		t.Fatal("unrelated CLI retained contexts were invalidated")
	}
}

func TestInformationFileToolsCannotWriteLibraryOrAliasInsideWorkspace(t *testing.T) {
	cwd := t.TempDir()
	store, err := information.Open(filepath.Join(cwd, "sessions", "information"), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.PutAsset("data.txt", "", strings.NewReader("original"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(cwd, "library-alias")
	if err = os.Symlink(store.Root(), alias); err != nil {
		t.Fatal(err)
	}
	registry := New(Options{Cwd: cwd, Information: &Information{Store: store, Cwd: cwd}})
	for _, target := range []string{filepath.Join(store.Root(), "assets", asset.SHA256, "data"), filepath.Join(alias, "assets", asset.SHA256, "data")} {
		for _, name := range []string{"write", "edit"} {
			tool, _ := registry.Get(name)
			args := map[string]any{"path": target, "content": "modified", "oldText": "original", "newText": "modified"}
			raw, _ := json.Marshal(args)
			if _, err = tool.Execute(context.Background(), raw); err == nil {
				t.Fatalf("%s allowed protected path %s", name, target)
			}
		}
	}
	copy, _, err := store.MaterializeAsset(asset.Asset, cwd)
	if err != nil {
		t.Fatal(err)
	}
	writer, _ := registry.Get("write")
	raw, _ := json.Marshal(map[string]any{"path": copy, "content": "modified"})
	if _, err = writer.Execute(context.Background(), raw); err != nil {
		t.Fatal("workspace copy became read-only: ", err)
	}
	original, _, err := store.AssetRead(asset.Asset)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	bytes := make([]byte, 8)
	n, _ := original.Read(bytes)
	if string(bytes[:n]) != "original" {
		t.Fatal("workspace edit mutated immutable library")
	}
}
