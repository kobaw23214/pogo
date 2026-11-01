package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skills"
)

// Seed the actual default discovery location in a temporary HOME. Upload
// validation must inspect only its archive, even when ordinary discovery has
// already installed user Skills. All writes stay under the test directories.
func ordinaryUploadWithExistingDefault(t *testing.T) (*harness, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pogo", "skills")
	target := filepath.Join(dir, "existing-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	existing := "---\nname: existing-skill\ndescription: Existing description.\n---\n\nEXISTING_BODY\n"
	if err := os.WriteFile(target, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	list, _ := skills.Load(skills.Options{UserDir: dir})
	if len(list) != 1 {
		t.Fatalf("temporary default catalog=%v", list)
	}
	h := newHarness(t, idleRespond, func(c *Config) { c.SkillOptions = skills.Options{UserDir: dir}; c.Skills = list })
	return h, dir, existing
}

func TestOrdinarySkillUploadIgnoresExistingDefaultCatalog(t *testing.T) {
	h, dir, existing := ordinaryUploadWithExistingDefault(t)
	incoming := "---\nname: incoming-skill\ndescription: Incoming description.\n---\n\nINCOMING_BODY\n"
	archive := webZip(t, []webZipEntry{{name: "incoming-skill/SKILL.md", data: []byte(incoming), mode: 0600}})
	runtimeSkillResponse(t, postSkillZip(t, h, archive), http.StatusCreated, nil)
	for name, want := range map[string]string{"existing-skill": existing, "incoming-skill": incoming} {
		got, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
		if err != nil || string(got) != want {
			t.Fatalf("%s changed/missing: %q %v", name, got, err)
		}
	}
	if len(h.mgr.Skills()) != 2 {
		t.Fatalf("installed catalog=%v", h.mgr.Skills())
	}
	if h.calls() != 0 {
		t.Fatal("upload invoked a model")
	}
}

func TestOrdinarySkillUploadSameNameUsesIncomingMetadata(t *testing.T) {
	h, dir, _ := ordinaryUploadWithExistingDefault(t)
	incoming := "---\nname: existing-skill\ndescription: Incoming replacement description.\n---\n\nREPLACEMENT_BODY\n"
	archive := webZip(t, []webZipEntry{{name: "existing-skill/SKILL.md", data: []byte(incoming), mode: 0600}})
	runtimeSkillResponse(t, postSkillZip(t, h, archive), http.StatusCreated, nil)
	list := h.mgr.Skills()
	if len(list) != 1 || list[0].Name != "existing-skill" || list[0].Description != "Incoming replacement description." || list[0].Path != filepath.Join(dir, "existing-skill", "SKILL.md") {
		t.Fatalf("replacement metadata=%v", list)
	}
	got, err := os.ReadFile(list[0].Path)
	if err != nil || string(got) != incoming {
		t.Fatalf("replacement content=%q %v", got, err)
	}
}

func TestOrdinarySkillUploadMalformedIncomingCannotUseExistingShadow(t *testing.T) {
	for _, incoming := range []string{
		"---\nname: existing-skill\n---\n\nNO_DESCRIPTION\n",
		"---\nname: existing-skill\ndescription: \n---\n\nEMPTY_DESCRIPTION\n",
	} {
		t.Run(strings.Split(incoming, "\n")[2], func(t *testing.T) {
			h, dir, existing := ordinaryUploadWithExistingDefault(t)
			archive := webZip(t, []webZipEntry{{name: "existing-skill/SKILL.md", data: []byte(incoming), mode: 0600}})
			res := postSkillZip(t, h, archive)
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("invalid archive: %d %s", res.StatusCode, body)
			}
			got, err := os.ReadFile(filepath.Join(dir, "existing-skill", "SKILL.md"))
			if err != nil || string(got) != existing {
				t.Fatalf("invalid upload overwrote existing Skill: %q %v", got, err)
			}
			if list := h.mgr.Skills(); len(list) != 1 || list[0].Description != "Existing description." {
				t.Fatalf("rejected upload changed catalog=%v", list)
			}
		})
	}
}
