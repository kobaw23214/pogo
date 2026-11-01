package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifest writes a tools.json into a fresh skill directory and returns
// the source pair LoadSynthTools wants.
func writeManifest(t *testing.T, skill, body string) SynthSource {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return SynthSource{Skill: skill, Dir: dir}
}

const goodTool = `{
  "tools": [
    {
      "name": "mal.decompile",
      "title": "样本反编译",
      "description": "Decompile a sample and return pseudocode.",
      "inputSchema": {
        "type": "object",
        "properties": {
          "sha256": {"type": "string", "description": "sample hash"},
          "arch":   {"type": "string", "enum": ["x86", "x64"]},
          "force":  {"type": "boolean"}
        },
        "required": ["sha256"]
      },
      "annotations": {"readOnlyHint": true, "idempotentHint": true},
      "x-pogo-exec": {
        "argv": ["malquery", "decompile", "--json"],
        "map": {
          "sha256": "append",
          "arch": {"flag": "--arch"},
          "force": {"flag": "--force", "presence": true}
        }
      },
      "x-pogo-renderer": "code"
    }
  ]
}`

func TestLoadSynthToolsValid(t *testing.T) {
	src := writeManifest(t, "mal", goodTool)
	got, catalog, diags := LoadSynthTools([]SynthSource{src})
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if len(got) != 1 || len(catalog) != 1 {
		t.Fatalf("want 1 tool and 1 catalog entry, got %d and %d", len(got), len(catalog))
	}
	// The dot cannot survive the wire: providers reject it outright.
	if name := got[0].Name(); name != "mal_decompile" {
		t.Fatalf("wire name = %q, want mal_decompile", name)
	}
	info := catalog[0]
	if info.Name != "mal.decompile" || info.WireName != "mal_decompile" {
		t.Fatalf("catalog names = %q / %q", info.Name, info.WireName)
	}
	if !info.ReadOnly || !info.Idempotent || !info.Registered || info.Renderer != "code" || info.Skill != "mal" {
		t.Fatalf("catalog projection wrong: %+v", info)
	}
	st := got[0].(*SynthTool)
	if st.IsReadOnly() != true {
		t.Fatal("IsReadOnly should follow readOnlyHint")
	}
	if st.ExecutionMode() != Parallel {
		t.Fatal("read-only idempotent tool should run Parallel")
	}
}

func TestLoadSynthToolsValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown field",
			body: `{"tools": [{"name": "x", "description": "d", "inputSchema": {"type": "object"},
				"x-pogo-exec": {"argv": ["true"]}, "surprise": 1}]}`,
			want: "unknown field",
		},
		{
			name: "name too long for the wire",
			body: `{"tools": [{"name": "` + strings.Repeat("a", 65) + `", "description": "d",
				"inputSchema": {"type": "object"}, "x-pogo-exec": {"argv": ["true"]}}]}`,
			want: "wire",
		},
		{
			name: "builtin collision",
			body: `{"tools": [{"name": "grep", "description": "d",
				"inputSchema": {"type": "object"}, "x-pogo-exec": {"argv": ["true"]}}]}`,
			want: "already taken",
		},
		{
			name: "collision via dot mapping",
			body: `{"tools": [
				{"name": "a.b", "description": "d", "inputSchema": {"type": "object"},
					"x-pogo-exec": {"argv": ["true"]}},
				{"name": "a_b", "description": "d", "inputSchema": {"type": "object"},
					"x-pogo-exec": {"argv": ["true"]}}
			]}`,
			want: "already taken",
		},
		{
			name: "control character in description",
			body: `{"tools": [{"name": "x", "description": "has a\u0000nul",
				"inputSchema": {"type": "object"}, "x-pogo-exec": {"argv": ["true"]}}]}`,
			want: "invisible",
		},
		{
			name: "unmapped property",
			body: `{"tools": [{"name": "x", "description": "d",
				"inputSchema": {"type": "object", "properties": {"q": {"type": "string"}}},
				"x-pogo-exec": {"argv": ["true"]}}]}`,
			want: "no x-pogo-exec.map entry",
		},
		{
			name: "boolean without presence",
			body: `{"tools": [{"name": "x", "description": "d",
				"inputSchema": {"type": "object", "properties": {"b": {"type": "boolean"}}},
				"x-pogo-exec": {"argv": ["true"], "map": {"b": "append"}}}]}`,
			want: "presence",
		},
		{
			name: "no exec",
			body: `{"tools": [{"name": "x", "description": "d", "inputSchema": {"type": "object"}}]}`,
			want: "x-pogo-exec is required",
		},
		{
			name: "required names undeclared property",
			body: `{"tools": [{"name": "x", "description": "d",
				"inputSchema": {"type": "object", "required": ["ghost"]},
				"x-pogo-exec": {"argv": ["true"]}}]}`,
			want: "ghost",
		},
		{
			name: "bad renderer",
			body: `{"tools": [{"name": "x", "description": "d", "inputSchema": {"type": "object"},
				"x-pogo-exec": {"argv": ["true"]}, "x-pogo-renderer": "hologram"}]}`,
			want: "renderer",
		},
		{
			name: "bad timeout",
			body: `{"tools": [{"name": "x", "description": "d", "inputSchema": {"type": "object"},
				"x-pogo-exec": {"argv": ["true"], "timeout_seconds": 9999}}]}`,
			want: "timeout_seconds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := writeManifest(t, "s", tc.body)
			got, _, diags := LoadSynthTools([]SynthSource{src})
			// The offending entry must not load; the diagnostic must say why
			// in words an author can act on.
			if len(diags) == 0 || !strings.Contains(diags[0].Message, tc.want) {
				t.Fatalf("want diagnostic mentioning %q, got %+v (tools loaded: %d)", tc.want, diags, len(got))
			}
		})
	}
}

func TestLoadSynthToolsSkillDirExpansion(t *testing.T) {
	body := `{"tools": [{"name": "x", "description": "d",
		"inputSchema": {"type": "object", "properties": {"f": {"type": "string"}}},
		"x-pogo-exec": {"argv": ["$SKILL_DIR/scripts/run", "--root", "$SKILL_DIR"], "map": {"f": "append"}}}]}`
	src := writeManifest(t, "s", body)
	got, _, diags := LoadSynthTools([]SynthSource{src})
	if len(diags) != 0 {
		t.Fatalf("diagnostics: %+v", diags)
	}
	st := got[0].(*SynthTool)
	want := []string{src.Dir + "/scripts/run", "--root", src.Dir}
	if strings.Join(st.exec.argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", st.exec.argv, want)
	}
}

func TestLoadSynthToolsUnregistered(t *testing.T) {
	body := `{"tools": [{"name": "x", "description": "d", "inputSchema": {"type": "object"},
		"x-pogo-exec": {"argv": ["true"]}, "x-pogo-registered": false}]}`
	src := writeManifest(t, "s", body)
	got, catalog, _ := LoadSynthTools([]SynthSource{src})
	if len(got) != 0 {
		t.Fatalf("unregistered tool was registered: %+v", got)
	}
	if len(catalog) != 1 || catalog[0].Registered {
		t.Fatalf("catalog should still list it as unregistered: %+v", catalog)
	}
}

func TestLoadSynthToolsRegisteredWarning(t *testing.T) {
	var toolsJSON strings.Builder
	toolsJSON.WriteString(`{"tools": [`)
	for i := 0; i < warnSynthRegistered+1; i++ {
		if i > 0 {
			toolsJSON.WriteByte(',')
		}
		toolsJSON.WriteString(`{"name": "t` + string(rune('a'+i)) + `", "description": "d",
			"inputSchema": {"type": "object"}, "x-pogo-exec": {"argv": ["true"]}}`)
	}
	toolsJSON.WriteString(`]}`)
	src := writeManifest(t, "s", toolsJSON.String())
	got, _, diags := LoadSynthTools([]SynthSource{src})
	if len(got) != warnSynthRegistered+1 {
		t.Fatalf("want %d registered tools, got %d", warnSynthRegistered+1, len(got))
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "degrades") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a count warning, got %+v", diags)
	}
}

func TestSynthToolsReadOnlyFilter(t *testing.T) {
	ro := writeManifest(t, "ro", `{"tools": [{"name": "q", "description": "d",
		"inputSchema": {"type": "object"}, "annotations": {"readOnlyHint": true},
		"x-pogo-exec": {"argv": ["true"]}}]}`)
	rw := writeManifest(t, "rw", `{"tools": [{"name": "w", "description": "d",
		"inputSchema": {"type": "object"},
		"x-pogo-exec": {"argv": ["true"]}}]}`)
	synth, _, _ := LoadSynthTools([]SynthSource{ro, rw})

	reg := New(Options{Cwd: t.TempDir(), Synth: synth})
	if _, ok := reg.Get("q"); !ok {
		t.Fatal("read-only tool missing from a normal session")
	}
	if _, ok := reg.Get("w"); !ok {
		t.Fatal("write tool missing from a normal session")
	}

	explore := New(Options{Cwd: t.TempDir(), ReadOnly: true, Synth: synth})
	if _, ok := explore.Get("q"); !ok {
		t.Fatal("read-only declared tool should reach a read-only session")
	}
	if _, ok := explore.Get("w"); ok {
		t.Fatal("non-read-only declared tool must not reach a read-only session")
	}
}
