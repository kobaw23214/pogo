package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skills"
)

func docsXLSXTool(t *testing.T) *SynthTool {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "skills", "builtin", "docs"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, diags := LoadSynthTools([]SynthSource{{Skill: "docs", Dir: dir}})
	if len(diags) != 0 {
		t.Fatalf("docs manifest: %+v", diags)
	}
	for _, tool := range loaded {
		st := tool.(*SynthTool)
		if st.info.Name == "docs.xlsx-sheet" {
			return st
		}
	}
	t.Fatal("docs.xlsx-sheet was not registered")
	return nil
}

func docsXLSXArgv(t *testing.T, st *SynthTool, args map[string]any) []string {
	t.Helper()
	raw := make(map[string]json.RawMessage, len(args))
	for key, value := range args {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		raw[key] = encoded
	}
	argv, err := st.buildArgv(raw)
	if err != nil {
		t.Fatal(err)
	}
	return argv
}

func TestDocsXLSXNamedArgv(t *testing.T) {
	st := docsXLSXTool(t)
	// The real runtime sorts map keys, not manifest declaration order.
	argv := docsXLSXArgv(t, st, map[string]any{
		"file": "uploads/结果.xlsx", "sheet": "高危IP列表-结果", "first_row": 2, "last_row": 50,
	})
	want := []string{"--file", "uploads/结果.xlsx", "--first-row", "2", "--last-row", "50", "--sheet", "高危IP列表-结果"}
	if !reflect.DeepEqual(argv[1:], want) {
		t.Fatalf("argv = %q, want %q", argv[1:], want)
	}
	// An omitted start bound must not shift another argument into its slot.
	argv = docsXLSXArgv(t, st, map[string]any{"file": "book.xlsx", "sheet": "数据", "last_row": 5})
	if !reflect.DeepEqual(argv[1:], []string{"--file", "book.xlsx", "--last-row", "5", "--sheet", "数据"}) {
		t.Fatalf("end-only argv: %q", argv)
	}
}

func docsXLSXPython(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{os.Getenv("POGO_TEST_PYTHON"), "/opt/venv/bin/python3", "python3"} {
		if candidate == "" {
			continue
		}
		executable, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		if err := exec.Command(executable, "-c", "import openpyxl").Run(); err == nil {
			return executable
		}
	}
	t.Skip("real .xlsx integration requires Python with openpyxl; set POGO_TEST_PYTHON")
	return ""
}

func TestDocsXLSXManifestScript(t *testing.T) {
	python := docsXLSXPython(t)
	st := docsXLSXTool(t)
	dir := t.TempDir()
	// Real workbooks: the actual script must reopen them with read_only mode.
	fixture := `
from pathlib import Path
from openpyxl import Workbook
import sys, zipfile
root = Path(sys.argv[1])
w = Workbook(); w.active.title = '首页'; w.active.append(['INDEX_ONE'])
s = w.create_sheet('高危IP列表-结果')
for i in range(1, 8): s.append([f'IP_ROW_{i}', f'证据_{i}', None])
w.create_sheet('2').append(['NUMERIC_TITLE'])
w.create_sheet('--last-row').append(['DASH_TITLE'])
w.save(root/'中文 工作簿.xlsx')
w = Workbook(); s = w.active; s.title = '大表'
for i in range(1, 12001): s.append([f'LARGE_ROW_{i}', i])
w.save(root/'large.xlsx')
w = Workbook(); s = w.active; s.title = '宽表'
for i in range(1, 31): s.append([f'C{j}_R{i}_' + 'x'*220 for j in range(1, 201)])
w.save(root/'wide.xlsx')
(root/'invalid.xlsx').write_bytes(b'not an xlsx')
with zipfile.ZipFile(root/'中文 工作簿.xlsx') as source, zipfile.ZipFile(root/'corrupt.xlsx', 'w') as target:
 for item in source.infolist():
  content = source.read(item.filename)
  if item.filename == 'xl/worksheets/sheet2.xml': content = content.replace(b'</sheetData>', b'<invalid></sheetData>')
  target.writestr(item, content)
`
	if output, err := exec.Command(python, "-c", fixture, dir).CombinedOutput(); err != nil {
		t.Fatalf("build workbook: %v\n%s", err, output)
	}
	run := func(t *testing.T, args map[string]any) (string, int) {
		t.Helper()
		argv := docsXLSXArgv(t, st, args)
		output, err := exec.Command(python, argv...).CombinedOutput()
		if err == nil {
			return string(output), 0
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return string(output), exit.ExitCode()
		}
		t.Fatalf("run script: %v", err)
		return "", -1
	}
	book := filepath.Join(dir, "中文 工作簿.xlsx")
	cases := []struct {
		name             string
		args             map[string]any
		contains, absent []string
		code             int
	}{
		{"Chinese title and exact rows", map[string]any{"file": book, "sheet": "高危IP列表-结果", "first_row": 2, "last_row": 3}, []string{"<!--行2-->", "IP_ROW_2", "<!--行3-->", "IP_ROW_3"}, []string{"IP_ROW_1", "IP_ROW_4", "Traceback"}, 0},
		{"end bound only", map[string]any{"file": book, "sheet": "高危IP列表-结果", "last_row": 2}, []string{"IP_ROW_1", "IP_ROW_2"}, []string{"IP_ROW_3"}, 0},
		{"start bound only", map[string]any{"file": book, "sheet": "高危IP列表-结果", "first_row": 6}, []string{"IP_ROW_6", "IP_ROW_7"}, []string{"IP_ROW_5", "已截断"}, 0},
		{"numeric title is not index", map[string]any{"file": book, "sheet": "2"}, []string{"NUMERIC_TITLE"}, []string{"IP_ROW_1"}, 0},
		{"sheet index", map[string]any{"file": book, "sheet": "1"}, []string{"INDEX_ONE"}, nil, 0},
		{"dash title remains value", map[string]any{"file": book, "sheet": "--last-row"}, []string{"DASH_TITLE"}, nil, 0},
		{"invalid integer is friendly", map[string]any{"file": book, "sheet": "高危IP列表-结果", "last_row": "高危IP列表-结果"}, []string{"参数错误", "--last-row", "整数"}, []string{"Traceback", "IP_ROW_"}, 2},
		{"reversed range", map[string]any{"file": book, "sheet": "高危IP列表-结果", "first_row": 5, "last_row": 2}, []string{"范围无效"}, []string{"Traceback", "IP_ROW_"}, 2},
		{"zero row", map[string]any{"file": book, "sheet": "高危IP列表-结果", "first_row": 0}, []string{"参数错误", "--first-row"}, []string{"Traceback"}, 2},
		{"out of range", map[string]any{"file": book, "sheet": "高危IP列表-结果", "first_row": 99}, []string{"超出工作表范围"}, []string{"Traceback"}, 2},
		{"unknown sheet", map[string]any{"file": book, "sheet": "不存在"}, []string{"没有 sheet", "高危IP列表-结果"}, []string{"Traceback"}, 2},
		{"default page is bounded", map[string]any{"file": filepath.Join(dir, "large.xlsx"), "sheet": "大表"}, []string{"LARGE_ROW_100 |", "下一批从行 101"}, []string{"LARGE_ROW_101 |", "LARGE_ROW_12000"}, 0},
		{"explicit large range is paged", map[string]any{"file": filepath.Join(dir, "large.xlsx"), "sheet": "大表", "first_row": 1001, "last_row": 9000}, []string{"LARGE_ROW_1001 |", "LARGE_ROW_2000 |", "下一批从行 2001"}, []string{"LARGE_ROW_2001 |"}, 0},
		{"default wide page explains limits", map[string]any{"file": filepath.Join(dir, "wide.xlsx"), "sheet": "宽表", "last_row": 1}, []string{"C30_R1_", "列已截断", "first_column=31", "单元格已截断"}, []string{"C31_R1_"}, 0},
		{"explicit column range", map[string]any{"file": filepath.Join(dir, "wide.xlsx"), "sheet": "宽表", "first_row": 2, "last_row": 2, "first_column": 31, "last_column": 32}, []string{"C31_R2_", "C32_R2_", "列 AE–AF"}, []string{"C30_R2_", "C33_R2_", "列已截断"}, 0},
		{"wide explicit range retains pagination", map[string]any{"file": filepath.Join(dir, "wide.xlsx"), "sheet": "宽表", "last_row": 1, "last_column": 200}, []string{"C100_R1_", "first_column=101"}, []string{"C101_R1_"}, 0},
		{"character budget includes anchors", map[string]any{"file": filepath.Join(dir, "wide.xlsx"), "sheet": "宽表", "last_row": 30, "last_column": 100}, []string{"字符预算用尽", "下一批从行"}, []string{"C1_R30_"}, 0},
		{"invalid file remains failure", map[string]any{"file": filepath.Join(dir, "invalid.xlsx"), "sheet": "x"}, []string{"打不开"}, []string{"Traceback"}, 1},
		{"lazy read corruption remains failure", map[string]any{"file": filepath.Join(dir, "corrupt.xlsx"), "sheet": "高危IP列表-结果"}, []string{"读取失败"}, []string{"Traceback", "IP_ROW_"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, code := run(t, tc.args)
			if code != tc.code {
				t.Fatalf("exit %d, want %d:\n%s", code, tc.code, output)
			}
			for _, fragment := range tc.contains {
				if !strings.Contains(output, fragment) {
					t.Errorf("missing %q in %s", fragment, output)
				}
			}
			for _, fragment := range tc.absent {
				if strings.Contains(output, fragment) {
					t.Errorf("unexpected %q in %s", fragment, output)
				}
			}
			if len([]rune(output)) > 120000 {
				t.Fatalf("output exceeded character budget: %d", len([]rune(output)))
			}
		})
	}
	t.Run("legacy CLI keeps positional contract", func(t *testing.T) {
		output, err := exec.Command(python, st.exec.argv[0], book, "高危IP列表-结果", "2", "3").CombinedOutput()
		if err != nil || !strings.Contains(string(output), "IP_ROW_2") || strings.Contains(string(output), "IP_ROW_4") {
			t.Fatalf("legacy CLI: %v\n%s", err, output)
		}
	})
}

func TestDocsXLSXEmbeddedCacheExecute(t *testing.T) {
	python := docsXLSXPython(t)
	cache, workspace := t.TempDir(), t.TempDir()
	docsDir := filepath.Join(cache, "docs")
	if err := os.MkdirAll(filepath.Join(docsDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"tools.json": `{"tools":[]}`, "scripts/xlsx-sheet": "stale script must never execute"} {
		if err := os.WriteFile(filepath.Join(docsDir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := skills.MaterializeEmbedded(cache); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(docsDir, "scripts", "xlsx-sheet")); err != nil || !strings.Contains(string(content), "--first-row") {
		t.Fatalf("stale embedded script was not replaced: %v", err)
	}
	loaded, _, diags := LoadSynthTools([]SynthSource{{Skill: "docs", Dir: docsDir}})
	if len(diags) != 0 {
		t.Fatalf("embedded manifest: %+v", diags)
	}
	var st *SynthTool
	for _, tool := range loaded {
		if tool.Name() == "docs_xlsx-sheet" {
			st = tool.(*SynthTool).WithWorkspace(workspace, workspace)
		}
	}
	if st == nil {
		t.Fatal("materialized xlsx tool missing")
	}
	fixture := "from openpyxl import Workbook; import sys; w=Workbook(); s=w.active; s.title='高危IP列表-结果'; s.append(['首行']); s.append(['嵌入缓存读取成功']); w.save(sys.argv[1])"
	if output, err := exec.Command(python, "-c", fixture, filepath.Join(workspace, "结果.xlsx")).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	// Select the test host's Python, leaving the embedded script, schema and
	// argument map untouched. Linux release uses the shipped /opt/venv shebang.
	st.exec.argv = append([]string{python}, st.exec.argv...)
	raw := json.RawMessage(`{"file":"结果.xlsx","sheet":"高危IP列表-结果","first_row":2,"last_row":2}`)
	if err := ValidateArgs(st, raw); err != nil {
		t.Fatalf("embedded schema: %v", err)
	}
	result, err := st.Execute(context.Background(), raw)
	if err != nil || !strings.Contains(result.Text, "嵌入缓存读取成功") || strings.Contains(result.Text, "首行") {
		t.Fatalf("embedded Execute: %v %+v", err, result)
	}
	if d := result.Details.(SynthDetails); d.ExitCode != 0 || !strings.Contains(strings.Join(d.Argv, " "), "--sheet 高危IP列表-结果") {
		t.Fatalf("embedded result details: %+v", d)
	}
	bad, err := st.Execute(context.Background(), json.RawMessage(`{"file":"结果.xlsx","sheet":"高危IP列表-结果","first_row":2,"last_row":1}`))
	if err == nil || !strings.Contains(err.Error(), "范围无效") || bad.Details.(SynthDetails).ExitCode != 2 {
		t.Fatalf("invalid range was not a failing tool result: %v %+v", err, bad)
	}
}
