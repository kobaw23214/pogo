package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var uploadTestBinding = nativeUploadBinding{"ctx_0123456789abcdef01234567", "t1", 1, 0, 1}

func uploadTestState(binding nativeUploadBinding) map[string]any {
	return map[string]any{"control": "agent", "context_ref": binding.Context, "active_tab": binding.Tab, "generation": binding.Generation, "control_epoch": binding.ControlEpoch, "document_version": binding.DocumentVersion}
}

func TestNativeUploadStreamsScopedBytes(t *testing.T) {
	root := t.TempDir()
	data := "部门,数量\n研发,12\n"
	if err := os.WriteFile(filepath.Join(root, "报告.csv"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	id := "11111111-1111-4111-8111-111111111111"
	staged := 0
	b := newNativeBrowser(nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			writeJSON(w, 200, uploadTestState(uploadTestBinding))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/uploads") {
			got, _ := io.ReadAll(r.Body)
			name, _ := url.PathUnescape(r.Header.Get("X-Upload-Filename"))
			if string(got) != data || name != "报告.csv" || r.ContentLength != int64(len([]byte(data))) {
				t.Error("file bytes or identity changed")
			}
			staged++
			writeJSON(w, 200, map[string]any{"ok": true, "file": map[string]any{"id": id, "size": len([]byte(data))}})
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
	}))
	b.uploadRoot = root
	op := map[string]json.RawMessage{"files": json.RawMessage(`["报告.csv"]`)}
	if err := b.prepareUpload(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if staged != 1 || string(op["files"]) != `["`+id+`"]` {
		t.Fatal("worker must only see opaque upload IDs")
	}
	var binding nativeUploadBinding
	if json.Unmarshal(op["upload_binding"], &binding) != nil || binding != uploadTestBinding {
		t.Fatal("worker must receive the original page identity")
	}
	unchanged, _ := os.ReadFile(filepath.Join(root, "报告.csv"))
	if string(unchanged) != data {
		t.Fatal("upload changed the original file")
	}
}

func TestNativeUploadCacheReuseAndContextRenewal(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "report.csv"), []byte("rows"), 0600)
	binding, stages := uploadTestBinding, 0
	ids := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
	b := newNativeBrowser(nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			writeJSON(w, 200, uploadTestState(binding))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/uploads") {
			io.Copy(io.Discard, r.Body)
			writeJSON(w, 200, map[string]any{"ok": true, "file": map[string]any{"id": ids[stages], "size": 4}})
			stages++
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
	}))
	b.uploadRoot = root
	for iteration := 0; iteration < 12; iteration++ {
		op := map[string]json.RawMessage{"files": json.RawMessage(`["report.csv"]`)}
		if err := b.prepareUpload(context.Background(), op); err != nil {
			t.Fatal(err)
		}
		if string(op["files"]) != `["`+ids[0]+`"]` {
			t.Fatal("unchanged file was not reused")
		}
	}
	if stages != 1 {
		t.Fatalf("unchanged file staged %d times", stages)
	}
	// Page changes may reuse the same staged bytes, but always carry the new
	// observed page identity. A context replacement needs a new worker UUID.
	binding.Tab = "t2"
	if err := b.prepareUpload(context.Background(), map[string]json.RawMessage{"files": json.RawMessage(`["report.csv"]`)}); err != nil {
		t.Fatal(err)
	}
	if stages != 1 {
		t.Fatal("page switch unnecessarily retransferred the file")
	}
	binding.Context = "ctx_abcdef0123456789abcdef01"
	op := map[string]json.RawMessage{"files": json.RawMessage(`["report.csv"]`)}
	if err := b.prepareUpload(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if stages != 2 || string(op["files"]) != `["`+ids[1]+`"]` {
		t.Fatal("a renewed context reused an expired file UUID")
	}
}

func TestNativeUploadPreparationFencesChangedPageAndCleansCache(t *testing.T) {
	for _, changed := range []string{"context", "tab", "generation", "control_epoch", "document", "human"} {
		t.Run(changed, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(filepath.Join(root, "report.csv"), []byte("rows"), 0600)
			binding, reads, staged, removed := uploadTestBinding, 0, false, 0
			id := "11111111-1111-4111-8111-111111111111"
			b := newNativeBrowser(nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/state") {
					reads++
					state := uploadTestState(binding)
					if staged {
						switch changed {
						case "context":
							state["context_ref"] = "ctx_abcdef0123456789abcdef01"
						case "tab":
							state["active_tab"] = "t2"
						case "generation":
							state["generation"] = 2
						case "control_epoch":
							state["control_epoch"] = 2 // human then Agent again
						case "document":
							state["document_version"] = 2
						case "human":
							state["control"] = "human"
						}
					}
					writeJSON(w, 200, state)
					return
				}
				if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/uploads/"+id) {
					removed++
					writeJSON(w, 200, map[string]any{"ok": true})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/uploads") {
					io.Copy(io.Discard, r.Body)
					staged = true
					writeJSON(w, 200, map[string]any{"ok": true, "file": map[string]any{"id": id, "size": 4}})
					return
				}
				t.Errorf("unexpected request %s", r.URL.Path)
			}))
			b.uploadRoot = root
			if err := b.prepareUpload(context.Background(), map[string]json.RawMessage{"files": json.RawMessage(`["report.csv"]`)}); err == nil {
				t.Fatal("changed upload page was accepted")
			}
			if reads != 2 || removed != 1 || len(b.uploadCache) != 0 {
				t.Fatalf("changed preparation leaked cache or bytes: reads=%d deletes=%d cache=%v", reads, removed, b.uploadCache)
			}
		})
	}
}

func TestNativeUploadSourceReplacementAndCachedMutationAreRejected(t *testing.T) {
	for _, mutation := range []string{"during-transfer", "cached-replacement", "cached-content"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "report.csv")
			os.WriteFile(path, []byte("rows"), 0600)
			info, _ := os.Stat(path)
			mutate := func() {
				if mutation == "cached-content" {
					os.WriteFile(path, []byte("edited content"), 0600)
					return
				}
				replacement := filepath.Join(root, "replacement.csv")
				os.WriteFile(replacement, []byte("rows"), 0600)
				os.Chtimes(replacement, info.ModTime(), info.ModTime())
				os.Rename(replacement, path)
			}
			states, removed, transferred := 0, 0, 0
			id := "11111111-1111-4111-8111-111111111111"
			b := newNativeBrowser(nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/state") {
					states++
					if mutation != "during-transfer" && states == 1 {
						mutate()
					}
					writeJSON(w, 200, uploadTestState(uploadTestBinding))
					return
				}
				if r.Method == http.MethodDelete {
					removed++
					writeJSON(w, 200, map[string]any{"ok": true})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/uploads") {
					transferred++
					io.Copy(io.Discard, r.Body)
					mutate()
					writeJSON(w, 200, map[string]any{"ok": true, "file": map[string]any{"id": id, "size": 4}})
					return
				}
				t.Errorf("unexpected request %s", r.URL.Path)
			}))
			b.uploadRoot = root
			if mutation != "during-transfer" {
				b.uploadCache = map[string]nativeUploadCacheEntry{path: {uploadTestBinding.Context, id, info, time.Now()}}
			}
			if err := b.prepareUpload(context.Background(), map[string]json.RawMessage{"files": json.RawMessage(`["report.csv"]`)}); err == nil || !strings.Contains(err.Error(), "source changed") {
				t.Fatalf("mutated source accepted: %v", err)
			}
			if mutation == "during-transfer" && (transferred != 1 || removed != 1 || len(b.uploadCache) != 0) {
				t.Fatal("source-change staging cleanup was incomplete")
			}
			if mutation != "during-transfer" && transferred != 0 {
				t.Fatal("cached source unnecessarily transferred")
			}
		})
	}
}

func TestNativeUploadCancelledPreparationCleansPriorStagingCache(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "one.csv"), []byte("rows"), 0600)
	os.WriteFile(filepath.Join(root, "two.csv"), []byte("rows"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stages, removed := 0, 0
	id := "11111111-1111-4111-8111-111111111111"
	b := newNativeBrowser(nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			writeJSON(w, 200, uploadTestState(uploadTestBinding))
			return
		}
		if r.Method == http.MethodDelete {
			removed++
			writeJSON(w, 200, map[string]any{"ok": true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/uploads") {
			stages++
			io.Copy(io.Discard, r.Body)
			if stages == 2 {
				cancel()
				<-r.Context().Done()
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "file": map[string]any{"id": id, "size": 4}})
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
	}))
	b.uploadRoot = root
	if err := b.prepareUpload(ctx, map[string]json.RawMessage{"files": json.RawMessage(`["one.csv","two.csv"]`)}); err == nil {
		t.Fatal("cancelled preparation accepted")
	}
	if stages != 2 || removed != 1 || len(b.uploadCache) != 0 {
		t.Fatalf("cancelled preparation leaked cache: stages=%d deletes=%d cache=%v", stages, removed, b.uploadCache)
	}
}

func TestNativeUploadRejectsEscapeAndHumanBeforeTransfer(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.csv")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.WriteFile(filepath.Join(root, "ok.csv"), []byte("okay"), 0600)
	os.Symlink(outside, filepath.Join(root, "escape.csv"))
	transfers := 0
	b := newNativeBrowser(nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/state") {
			writeJSON(w, 200, map[string]any{"control": "human"})
			return
		}
		transfers++
	}))
	b.uploadRoot = root
	for _, paths := range [][]string{{"ok.csv", outside}, {"escape.csv"}, {"ok.csv"}} {
		raw, _ := json.Marshal(paths)
		if err := b.prepareUpload(context.Background(), map[string]json.RawMessage{"files": raw}); err == nil {
			t.Fatal("unsafe upload accepted")
		}
	}
	if transfers != 0 {
		t.Fatal("rejected upload transferred bytes")
	}
}
