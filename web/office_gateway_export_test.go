package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOfficeGatewayExportMappingKeepsOnlyEditorNamespace(t *testing.T) {
	service := &officeService{cfg: &officeConfig{browserURL: "https://pogo.example/office/editor", browserOrigin: "https://pogo.example", serviceURL: "http://pogo-office:8080", serviceOrigin: "http://pogo-office:8080"}}
	for _, test := range []struct{ from, want string }{
		{"https://pogo.example/office/editor/cache/files/key/output.xlsx?md5=signed&expires=123", "http://pogo-office:8080/cache/files/key/output.xlsx?md5=signed&expires=123"},
		{"http://pogo-office:8080/cache/files/key/output.docx?token=opaque", "http://pogo-office:8080/cache/files/key/output.docx?token=opaque"},
	} {
		got, err := service.exportURL(test.from, "https://pogo.example")
		if err != nil || got.String() != test.want {
			t.Fatalf("export mapping: got=%v err=%v want=%s", got, err, test.want)
		}
	}
	for _, source := range []string{
		"https://pogo.example/api/files/content", "https://pogo.example/office/files/id/cap",
		"https://pogo.example/office/editor-other/cache/files/key/output.xlsx", "https://pogo.example/office/editor",
		"https://pogo.example/office/editor/../api/files", "https://pogo.example/office/editor/%2e%2e/api/files",
		"https://pogo.example/office/editor%2fcache/files", "https://pogo.example/office/editor/cache%5cfiles/key/output.xlsx",
		"https://other.example/office/editor/cache/files/key/output.xlsx", "http://pogo.example/office/editor/cache/files/key/output.xlsx",
	} {
		if _, err := service.exportURL(source, "https://pogo.example"); err == nil {
			t.Fatalf("unapproved export source accepted: %s", source)
		}
	}
}

func TestOfficeTunnelConfigKeepsInternalAddressesWithoutPublicURL(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "office-secret")
	if err := os.WriteFile(secret, []byte(strings.Repeat("test-secret", 8)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, public := range []string{"", "http://192.0.2.44:19770"} {
		cfg, err := parseOfficeConfig(&OfficeOptions{ServiceURL: "http://pogo-office:8080", FetchURL: "http://pogo:7777", PublicURL: public, JWTSecretFile: secret})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.browserURL != officeGatewayPrefix || cfg.serviceURL != "http://pogo-office:8080" || cfg.fetchURL != "http://pogo:7777" || cfg.browserOrigin != "" {
			t.Fatal("configured server origins escaped into a browser-facing URL")
		}
	}
	if _, err := parseOfficeConfig(&OfficeOptions{ServiceURL: "http://pogo-office:8080", JWTSecretFile: secret}); err == nil {
		t.Fatal("missing internal fetch address was guessed from the browser")
	}
}

func TestOfficeTunnelExportUsesOnlyBoundViewerAndInternalUpstream(t *testing.T) {
	service := &officeService{cfg: &officeConfig{publicURL: "http://192.0.2.44:19770", browserOrigin: "http://192.0.2.44:19770", serviceURL: "http://pogo-office:8080", serviceOrigin: "http://pogo-office:8080"}}
	for _, test := range []struct{ source, origin, want string }{
		{"http://localhost:8080/office/editor/cache/files/data/pogo-test_22/output.docx?md5=opaque", "http://localhost:8080", "http://pogo-office:8080/cache/files/data/pogo-test_22/output.docx?md5=opaque"},
		{"/office/editor/cache/files/data/pogo-test_22/output.xlsx", "http://localhost:8080", "http://pogo-office:8080/cache/files/data/pogo-test_22/output.xlsx"},
		{"https://pogo.example/office/editor/cache/files/data/pogo-test_22/output.xlsx", "https://pogo.example", "http://pogo-office:8080/cache/files/data/pogo-test_22/output.xlsx"},
		{"http://pogo-office:8080/cache/files/data/pogo-test_22/output.xlsx", "http://localhost:8080", "http://pogo-office:8080/cache/files/data/pogo-test_22/output.xlsx"},
	} {
		got, err := service.exportURL(test.source, test.origin)
		if err != nil || got.String() != test.want {
			t.Fatalf("tunneled export mapping got=%v err=%v want=%s", got, err, test.want)
		}
	}
	for _, rejected := range []string{
		"http://192.0.2.44:19770/office/editor/cache/files/data/pogo-test_22/output.docx",
		"http://localhost:8081/office/editor/cache/files/data/pogo-test_22/output.docx",
		"http://localhost:8080/api/files/content", "//other.example/office/editor/cache/files/data/pogo-test_22/output.docx",
		"/cache/files/data/pogo-test_22/output.docx", "file:///office/editor/cache/files/data/pogo-test_22/output.docx",
	} {
		if _, err := service.exportURL(rejected, "http://localhost:8080"); err == nil {
			t.Fatalf("export escaped its viewer binding: %s", rejected)
		}
	}
	if _, err := service.exportURL("/office/editor/cache/files/data/pogo-test_22/output.docx", ""); err == nil {
		t.Fatal("relative export admitted without an editor origin binding")
	}

	service, document, file := officeTestService(t)
	instance := officeTestReady(t, service, document)
	updated := officeTestPackage(t, "human and agent saved through tunnel")
	var requests atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.HasPrefix(r.URL.Path, "/cache/files/data/"+document.key+"_") || r.URL.Query().Get("md5") != "opaque" {
			t.Error("export fetch did not remain on the current internal document path")
		}
		w.Write(updated)
	}))
	defer engine.Close()
	service.cfg.serviceOrigin, service.cfg.serviceURL = engine.URL, engine.URL
	document.mu.Lock()
	document.browserOrigin = "http://localhost:8080"
	document.mu.Unlock()
	reply := make(chan error, 1)
	go func() {
		_, err := service.save(context.Background(), document, "save-tunnel-001")
		reply <- err
	}()
	officeTestQueued(t, document, "save-tunnel-001")
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8080/office/bridge/"+document.id+"/"+document.bridgeToken+"/next?instanceId="+instance, nil)
	request.SetPathValue("oid", document.id)
	request.SetPathValue("cap", document.bridgeToken)
	request.SetPathValue("resource", "next")
	response := httptest.NewRecorder()
	(&Server{office: service}).handleOfficeBridge(response, request)
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte("save-tunnel-001")) {
		t.Fatal("save was not dispatched to its current tunneled viewer")
	}
	if err := service.exportCurrent(context.Background(), document, instance, "save-tunnel-001", "docx", "http://localhost:8080/office/editor/cache/files/data/"+document.key+"_22/output.docx?md5=opaque"); err != nil {
		t.Fatal(err)
	}
	if err := <-reply; err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(actual, updated) || requests.Load() != 1 {
		t.Fatal("tunneled save did not persist its sole internal export")
	}
}

func TestOfficeExportCacheIsBoundToCurrentDocument(t *testing.T) {
	for _, resource := range []string{
		"/cache/files/pogo-document/output.docx",
		"/cache/files/pogo-document_2756/output.xlsx",
		"/cache/files/data/pogo-document_2756/output.docx",
		"/cache/files/data/conv_pogo-document_xlsx/output.xlsx",
	} {
		if !officeExportDocumentPath(resource, "pogo-document") {
			t.Fatalf("current document export rejected: %s", resource)
		}
	}
	for _, resource := range []string{
		"/cache/files/data/another-document_2756/output.docx",
		"/cache/files/data/pogo-document-other_2756/output.docx",
		"/cache/files/data/conv_another-document_xlsx/output.xlsx",
		"/cache/files/forgotten/pogo-document/output.docx",
		"/cache/files/data/pogo-document", "/cache/files/data/",
		"/export", "/doc/pogo-document/output.docx",
	} {
		if officeExportDocumentPath(resource, "pogo-document") {
			t.Fatalf("unbound document export accepted: %s", resource)
		}
	}

	service, document, file := officeTestService(t)
	instance := officeTestReady(t, service, document)
	original, _ := os.ReadFile(file)
	var fetches atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Write(officeTestPackage(t, "other document"))
	}))
	defer engine.Close()
	service.cfg.browserOrigin, service.cfg.serviceOrigin, service.cfg.serviceURL = engine.URL, engine.URL, engine.URL
	reply := officeTestStartSave(t, service, document, instance, "save-cross-doc-001")
	err := service.exportCurrent(context.Background(), document, instance, "save-cross-doc-001", "docx", engine.URL+"/cache/files/data/another-document_2756/output.docx")
	officeExpectError(t, err, "EXPORT_DOCUMENT_REJECTED")
	if <-reply == nil || fetches.Load() != 0 {
		t.Fatal("another document export was fetched or reported saved")
	}
	current, _ := os.ReadFile(file)
	if !bytes.Equal(current, original) || document.view().SaveState != "error" {
		t.Fatal("rejected export changed the original file or retained a pending save")
	}
}
