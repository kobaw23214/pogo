package schedule

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// isolateConfig points providers.json at a file that does not exist, so
// collectKeyEnvs finds nothing and generated units carry no real credentials.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("POGO_CONFIG", filepath.Join(t.TempDir(), "no-providers.json"))
}

// fakeExec swaps the command seam for one that records argv and (with
// captureStdinInto) pipes stdin to a file. No test in this package may run
// launchctl/systemctl/crontab for real.
func fakeExec(t *testing.T, captureStdinInto string) *[]recordedCmd {
	t.Helper()
	recorded := &[]recordedCmd{}
	restore := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		*recorded = append(*recorded, recordedCmd{name: name, args: args})
		if captureStdinInto != "" {
			return exec.Command("sh", "-c", "cat > \"$0\"", captureStdinInto)
		}
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = restore })
	return recorded
}

type recordedCmd struct {
	name string
	args []string
}

func (c recordedCmd) String() string { return c.name + " " + strings.Join(c.args, " ") }

func TestCalendarIntervals(t *testing.T) {
	cases := []struct {
		expr string
		// Each dict as "key=value" pairs in order; the expansion order is
		// Month, Day, Weekday, Hour, Minute with star keys omitted.
		want [][]string
	}{
		{"0 9 * * *", [][]string{{"Hour=9", "Minute=0"}}},
		{"*/15 * * * *", [][]string{{"Minute=0"}, {"Minute=15"}, {"Minute=30"}, {"Minute=45"}}},
		{"0 9 * * 1-5", [][]string{
			{"Weekday=1", "Hour=9", "Minute=0"},
			{"Weekday=2", "Hour=9", "Minute=0"},
			{"Weekday=3", "Hour=9", "Minute=0"},
			{"Weekday=4", "Hour=9", "Minute=0"},
			{"Weekday=5", "Hour=9", "Minute=0"},
		}},
		// DOM and DOW restricted together stay in one dict: launchd ORs
		// exactly Day and Weekday, which is cron's own rule.
		{"0 0 13 * 5", [][]string{{"Day=13", "Weekday=5", "Hour=0", "Minute=0"}}},
		// An all-star field is an absent key, so @hourly is one dict.
		{"@hourly", [][]string{{"Minute=0"}}},
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatal(err)
		}
		dicts, err := calendarIntervals(s)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		var got [][]string
		for _, d := range dicts {
			var pairs []string
			for _, kv := range d.dict {
				pairs = append(pairs, kv.key+"="+itoa(*kv.val.num))
			}
			got = append(got, pairs)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestCalendarIntervalsRefusesExplosions(t *testing.T) {
	s, err := Parse("*/2 */2 */2 * *") // 30 × 12 × 16 = 5760 dicts
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calendarIntervals(s); err == nil {
		t.Fatal("an expression expanding to thousands of dicts was accepted")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestCronToOnCalendar(t *testing.T) {
	cases := []struct {
		expr string
		want []string
	}{
		{"0 9 * * *", []string{"*-*-* 9:0:00"}},
		{"*/15 * * * *", []string{"*-*-* *:0,15,30,45:00"}},
		{"0 9 * * 1-5", []string{"Mon..Fri *-*-* 9:0:00"}},
		{"0 0 1 jan *", []string{"*-1-1 0:0:00"}},
		// DOM and DOW both restricted: systemd ANDs them where cron ORs, so
		// the line splits in two (OnCalendar lines are themselves OR'd).
		{"0 0 13 * 5", []string{"*-*-13 0:0:00", "Fri *-*-* 0:0:00"}},
		{"30 4 1,15 * 1", []string{"*-*-1,15 4:30:00", "Mon *-*-* 4:30:00"}},
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatal(err)
		}
		got, err := cronToOnCalendar(s)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestLaunchdPlistIsWellFormed(t *testing.T) {
	isolateConfig(t)
	b := &launchdBackend{dir: t.TempDir(), warn: io.Discard}
	job := testJob("abc12345")
	job.Cron = "0 9 * * 1-5"
	data, err := b.generate(job, "/usr/local/bin/pogo")
	if err != nil {
		t.Fatal(err)
	}

	// Well-formed XML with a plist root, whatever our own writer produced.
	var doc struct {
		XMLName xml.Name
		Inner   string `xml:",innerxml"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("generated plist is not well-formed XML: %v\n%s", err, data)
	}
	if doc.XMLName.Local != "plist" {
		t.Fatalf("root element is %q, want plist", doc.XMLName.Local)
	}

	text := string(data)
	for _, want := range []string{
		"<key>Label</key>",
		"<string>ai.pogo.schedule.abc12345</string>",
		"<key>ProgramArguments</key>",
		"<string>/usr/local/bin/pogo</string>",
		"<string>-schedule-exec</string>",
		"<string>abc12345</string>",
		"<key>RunAtLoad</key>",
		"<key>StartCalendarInterval</key>",
		"<key>Weekday</key>",
		"<key>EnvironmentVariables</key>",
		"<key>PATH</key>",
		"<key>WorkingDirectory</key>",
		"<string>/tmp/work</string>",
		"<key>StandardOutPath</key>",
		"<key>StandardErrorPath</key>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist is missing %s\n%s", want, text)
		}
	}
	// Escaping is the reason the plist goes through encoding/xml at all.
	job.Prompt = "a <b> & \"quoted\" prompt"
	job.Workdir = "/tmp/a & b"
	if _, err := b.generate(job, "/usr/local/bin/pogo"); err != nil {
		t.Fatal(err)
	}
	// launchd's parser rejects <false></false> (bootstrap error 109); the
	// DTD declares booleans EMPTY, so they must be self-closing.
	if strings.Contains(text, "<false></false>") || strings.Contains(text, "<true></true>") {
		t.Errorf("plist contains expanded boolean elements launchd cannot parse\n%s", text)
	}
	if !strings.Contains(text, "<false/>") {
		t.Errorf("plist is missing self-closing <false/>\n%s", text)
	}
}

func TestLaunchdEveryAndAt(t *testing.T) {
	isolateConfig(t)
	b := &launchdBackend{dir: t.TempDir(), warn: io.Discard}

	every := testJob("eeeeeeee")
	every.Kind, every.EverySec, every.Cron = KindEvery, 1800, ""
	data, err := b.generate(every, "/usr/local/bin/pogo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<key>StartInterval</key>") {
		t.Errorf("every job got no StartInterval:\n%s", data)
	}

	at := testJob("aaaaaaaa")
	at.Kind, at.At, at.Cron = KindAt, "2026-08-23T09:00:00+08:00", ""
	data, err = b.generate(at, "/usr/local/bin/pogo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"<key>Month</key>", "<key>Day</key>", "<key>Hour</key>"} {
		if !strings.Contains(text, want) {
			t.Errorf("at job's calendar interval is missing %s\n%s", want, text)
		}
	}
}

func TestLaunchdRegisterSequence(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	restore := launchAgentsDir
	launchAgentsDir = func() string { return dir }
	t.Cleanup(func() { launchAgentsDir = restore })
	recorded := fakeExec(t, "")

	b := &launchdBackend{dir: t.TempDir(), warn: io.Discard}
	if err := b.Register(testJob("abc12345"), "/usr/local/bin/pogo"); err != nil {
		t.Fatal(err)
	}

	plist := filepath.Join(dir, "ai.pogo.schedule.abc12345.plist")
	st, err := os.Stat(plist)
	if err != nil {
		t.Fatalf("plist was not written: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("plist perm = %o, want 600 (it can hold API keys)", perm)
	}

	var bootout, lint, bootstrap bool
	for _, c := range *recorded {
		s := c.String()
		switch {
		case c.name == "launchctl" && strings.HasPrefix(s, "launchctl bootout"):
			bootout = true
		case c.name == "plutil":
			lint = true
		case c.name == "launchctl" && strings.HasPrefix(s, "launchctl bootstrap"):
			bootstrap = true
		}
	}
	if !bootout || !lint || !bootstrap {
		t.Errorf("register sequence = %v; want bootout, plutil -lint, bootstrap", *recorded)
	}

	// Unregister bootouts and removes the file.
	*recorded = nil
	if err := b.Unregister(testJob("abc12345")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("plist survived Unregister")
	}
}

func TestSystemdUnits(t *testing.T) {
	isolateConfig(t)
	b := &systemdBackend{dir: t.TempDir(), warn: io.Discard}

	cron := testJob("abc12345")
	cron.Cron = "0 9 * * *"
	svc, tmr, err := b.generate(cron, "/usr/local/bin/pogo")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Type=oneshot",
		"WorkingDirectory=/tmp/work",
		"ExecStart=/usr/local/bin/pogo -schedule-exec abc12345",
		"NoNewPrivileges=yes",
		"StandardOutput=append:",
		"Environment=\"PATH=",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("service unit is missing %q:\n%s", want, svc)
		}
	}
	for _, want := range []string{
		"OnCalendar=*-*-* 9:0:00",
		"Persistent=true",
		"WantedBy=timers.target",
	} {
		if !strings.Contains(tmr, want) {
			t.Errorf("timer unit is missing %q:\n%s", want, tmr)
		}
	}

	every := testJob("eeeeeeee")
	every.Kind, every.EverySec, every.Cron = KindEvery, 1800, ""
	_, tmr, err = b.generate(every, "/usr/local/bin/pogo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tmr, "OnUnitActiveSec=1800s") {
		t.Errorf("every timer is missing OnUnitActiveSec:\n%s", tmr)
	}

	at := testJob("aaaaaaaa")
	at.Kind, at.At, at.Cron = KindAt, "2026-08-23T09:00:00Z", ""
	_, tmr, err = b.generate(at, "/usr/local/bin/pogo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tmr, "OnCalendar=2026-08-23 09:00:00") {
		t.Errorf("at timer is missing the one-shot OnCalendar:\n%s", tmr)
	}
}

func TestCrontabLineAndBlock(t *testing.T) {
	cron := testJob("abc12345")
	line, err := crontabLine(cron, "/usr/local/bin/pogo", "/home/u/.pogo/schedule/logs/abc12345.out.log")
	if err != nil {
		t.Fatal(err)
	}
	want := "0 9 * * * cd '/tmp/work' && '/usr/local/bin/pogo' -schedule-exec 'abc12345' >> '/home/u/.pogo/schedule/logs/abc12345.out.log' 2>&1"
	if line != want {
		t.Errorf("crontabLine =\n%s\nwant\n%s", line, want)
	}

	// every degrades to the nearest minute step.
	every := testJob("eeeeeeee")
	every.Kind, every.EverySec, every.Cron = KindEvery, 1800, ""
	line, err = crontabLine(every, "/p", "/l")
	if err != nil || !strings.HasPrefix(line, "*/30 * * * * ") {
		t.Errorf("every 30m line = %q, %v", line, err)
	}
	every.EverySec = 30
	if _, err = crontabLine(every, "/p", "/l"); err == nil {
		t.Error("a sub-minute interval was accepted by the cron backend")
	}

	// The block strips cleanly, leaving other entries alone.
	content := "# user entry\n0 1 * * * /usr/bin/backup\n" +
		"# BEGIN ai.pogo.schedule.abc12345\n0 9 * * * cd '/tmp/work' && /p -schedule-exec abc12345\n# END ai.pogo.schedule.abc12345\n" +
		"# BEGIN ai.pogo.schedule.ffffffff\n0 10 * * * /p -schedule-exec ffffffff\n# END ai.pogo.schedule.ffffffff\n"
	stripped := stripCrontabBlock(content, "abc12345")
	if strings.Contains(stripped, "abc12345") {
		t.Errorf("block for abc12345 survived:\n%s", stripped)
	}
	if !strings.Contains(stripped, "/usr/bin/backup") || !strings.Contains(stripped, "ffffffff") {
		t.Errorf("unrelated entries were stripped:\n%s", stripped)
	}
}

func TestCronBackendRegisterWritesCrontab(t *testing.T) {
	isolateConfig(t)
	capture := filepath.Join(t.TempDir(), "crontab")
	recorded := fakeExec(t, capture)
	b := &cronBackend{dir: t.TempDir(), warn: io.Discard}
	if err := b.Register(testJob("abc12345"), "/usr/local/bin/pogo"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("the new crontab never reached stdin: %v (commands: %v)", err, *recorded)
	}
	text := string(data)
	if !strings.Contains(text, "# BEGIN ai.pogo.schedule.abc12345") ||
		!strings.Contains(text, "0 9 * * * cd '/tmp/work'") {
		t.Errorf("installed crontab is wrong:\n%s", text)
	}
}

func TestDetectForcedByEnv(t *testing.T) {
	for name, want := range map[string]string{"launchd": "launchd", "systemd": "systemd", "cron": "cron", "noop": "noop"} {
		t.Setenv(BackendEnv, name)
		b, err := Detect(t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b.Name() != want {
			t.Errorf("%s=%s: backend is %q", BackendEnv, name, b.Name())
		}
	}
	t.Setenv(BackendEnv, "bogus")
	if _, err := Detect(t.TempDir()); err == nil {
		t.Errorf("%s=bogus: want an error", BackendEnv)
	}
}
