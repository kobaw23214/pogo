package schedule

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/config"
)

// Backend is the bridge between a job and the OS scheduler. Register installs
// a trigger that will run `execPath -schedule-exec <id>`; Unregister removes
// it. Nothing here runs the job itself.
type Backend interface {
	Name() string
	Register(job Job, execPath string) error
	Unregister(job Job) error
}

// BackendEnv forces a backend, for tests and for machines where detection
// picks wrong (a systemd inside a container that cannot own user units, say).
// "noop" installs no OS trigger at all — for development and tests only.
// "internal" is the in-process trigger: the store is the registration and the
// web server's timer fires the jobs (see Internal).
const BackendEnv = "POGO_SCHEDULE_BACKEND"

// ErrNoScheduler says this machine has no OS scheduler to hand triggers to —
// an environment, not a misconfiguration. Wrapped by every Detect failure
// that means "nothing here to register with" (a container without systemd or
// cron, windows), so a caller can tell it from a bad POGO_SCHEDULE_BACKEND
// value and pick the in-process fallback rather than refusing.
var ErrNoScheduler = errors.New("no usable scheduler")

// execCommand is the seam tests replace: backend registration must never
// shell out in a test.
var execCommand = exec.Command

// Detect picks the backend for this machine: launchd on macOS; on Linux,
// systemd when a user manager answers, else the user's crontab when the
// crontab binary exists. Windows has no backend.
//
// dir is the schedule state directory — the backends point their log paths
// into it, which is why detection needs it.
func Detect(dir string) (Backend, error) {
	if forced := strings.TrimSpace(os.Getenv(BackendEnv)); forced != "" {
		switch forced {
		case "launchd":
			return &launchdBackend{dir: dir, warn: os.Stderr}, nil
		case "systemd":
			return &systemdBackend{dir: dir, warn: os.Stderr}, nil
		case "cron":
			return &cronBackend{dir: dir, warn: os.Stderr}, nil
		case "noop":
			return noopBackend{}, nil
		case "internal":
			return Internal(), nil
		default:
			return nil, fmt.Errorf("%s=%q: want launchd, systemd, cron, internal or noop", BackendEnv, forced)
		}
	}
	switch runtime.GOOS {
	case "darwin":
		return &launchdBackend{dir: dir, warn: os.Stderr}, nil
	case "linux":
		if err := execCommand("systemctl", "--user", "show-environment").Run(); err == nil {
			return &systemdBackend{dir: dir, warn: os.Stderr}, nil
		}
		if _, err := exec.LookPath("crontab"); err == nil {
			return &cronBackend{dir: dir, warn: os.Stderr}, nil
		}
		return nil, fmt.Errorf("%w: systemd --user does not answer and no crontab binary is on PATH", ErrNoScheduler)
	case "windows":
		return nil, fmt.Errorf("%w: launchd, systemd and cron backends do not exist on windows", ErrNoScheduler)
	default:
		return nil, fmt.Errorf("%w: none is implemented for %s", ErrNoScheduler, runtime.GOOS)
	}
}

// label is the name every backend addresses a job by: launchd's Label,
// systemd's unit basename, the crontab marker block.
func label(id string) string { return "ai.pogo.schedule." + id }

// noopBackend installs nothing. It exists for tests and for development —
// the web API can be exercised end to end without touching launchd, systemd
// or a crontab — and is only ever chosen explicitly, through
// POGO_SCHEDULE_BACKEND=noop. Jobs save fine and run-now works, but no OS
// trigger ever fires: on a real deployment this is the wrong backend.
type noopBackend struct{}

func (noopBackend) Name() string               { return "noop" }
func (noopBackend) Register(Job, string) error { return nil }
func (noopBackend) Unregister(Job) error       { return nil }

// Internal is the backend with no OS scheduler behind it: the store itself is
// the registration, and whoever wants these jobs to fire must be the one
// watching the clock — the web server's timer (web/scheduler.go) is. It is
// the fallback Detect failures map to in web mode (a container has neither
// systemd nor cron, and Windows has neither either), and the deliberate
// choice via POGO_SCHEDULE_BACKEND=internal for a deployment that wants the
// long-lived web process to own firing instead of the OS. Same no-op shape as
// noopBackend under a name that tells the panel the truth about who fires.
func Internal() Backend { return internalBackend{} }

type internalBackend struct{}

func (internalBackend) Name() string               { return "internal" }
func (internalBackend) Register(Job, string) error { return nil }
func (internalBackend) Unregister(Job) error       { return nil }

// collectKeyEnvs gathers the API keys providers.json refers to by name, from
// this process's environment.
//
// The OS schedulers run the job with a bare environment: none of the user's
// shell exports are there, and a provider configured as
// {"key_env": "ZHIPU_API_KEY"} would come up keyless. Registration therefore
// copies the *values* it can see into the unit file. That is a deliberate
// widening of where the secret lives (environment → environment plus a 0600
// file), which is why `add` says it out loud.
//
// The file is decoded directly rather than via config.Load: Load merges and
// validates the whole catalog (and fails without models), while all this
// needs is every key_env name that appears, valid catalog or not.
func collectKeyEnvs() map[string]string {
	out := map[string]string{}
	path, err := config.Path()
	if err != nil {
		return out
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var f config.FileShape
	if err := json.Unmarshal(raw, &f); err != nil {
		return out
	}
	for _, p := range f.Providers {
		if p.KeyEnv == "" {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(p.KeyEnv)); v != "" {
			out[p.KeyEnv] = v
		}
	}
	return out
}

// KeyEnvNames lists the key_env names providers.json refers to *and* this
// process's environment provides — the names only, never the values. The CLI
// uses it for the add-time disclosure that keys were copied into the OS unit
// file, so it must name exactly what collectKeyEnvs would copy.
func KeyEnvNames() []string {
	names := []string{}
	path, err := config.Path()
	if err != nil {
		return names
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return names
	}
	var f config.FileShape
	if err := json.Unmarshal(raw, &f); err != nil {
		return names
	}
	seen := map[string]bool{}
	for _, p := range f.Providers {
		if p.KeyEnv == "" || seen[p.KeyEnv] {
			continue
		}
		if strings.TrimSpace(os.Getenv(p.KeyEnv)) == "" {
			continue
		}
		seen[p.KeyEnv] = true
		names = append(names, p.KeyEnv)
	}
	sort.Strings(names)
	return names
}

// jobEnvironment is what the backends give the child: HOME and PATH (launchd
// gives the child almost nothing without them), plus every provider key
// collectKeyEnvs could see.
func jobEnvironment() map[string]string {
	env := map[string]string{}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		env["HOME"] = home
	}
	env["PATH"] = os.Getenv("PATH")
	for k, v := range collectKeyEnvs() {
		env[k] = v
	}
	return env
}

// ---------------------------------------------------------------------------
// launchd
// ---------------------------------------------------------------------------

// launchAgentsDir is a variable so tests can point it at a temp directory
// instead of the real ~/Library/LaunchAgents.
var launchAgentsDir = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents")
}

type launchdBackend struct {
	dir  string
	warn io.Writer
}

func (b *launchdBackend) Name() string { return "launchd" }

func (b *launchdBackend) plistPath(id string) string {
	return filepath.Join(launchAgentsDir(), label(id)+".plist")
}

func (b *launchdBackend) Register(job Job, execPath string) error {
	data, err := b.generate(job, execPath)
	if err != nil {
		return err
	}
	dir := launchAgentsDir()
	if dir == "" {
		return fmt.Errorf("no home directory: cannot place a LaunchAgent")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// launchd refuses to bootstrap a job whose StandardOutPath directory is
	// missing (Bootstrap failed: 5), so create the logs dir up front.
	if err := os.MkdirAll(filepath.Join(b.dir, "logs"), 0o700); err != nil {
		return err
	}
	plist := b.plistPath(job.ID)
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	// Replace, not fail: a re-add of the same id updates the trigger.
	// bootout errors (not loaded) are expected and ignored.
	execCommand("launchctl", "bootout", domain+"/"+label(job.ID)).Run()
	// 0600: the plist carries API keys (see collectKeyEnvs).
	if err := os.WriteFile(plist, data, 0o600); err != nil {
		return err
	}
	if out, err := execCommand("plutil", "-lint", plist).CombinedOutput(); err != nil {
		os.Remove(plist)
		return fmt.Errorf("plutil -lint %s: %v: %s", plist, err, out)
	}
	if out, err := execCommand("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		os.Remove(plist)
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	if job.Kind == KindEvery {
		fmt.Fprintf(b.warn, "warning: job %s: launchd StartInterval jobs that fall due while the machine sleeps are missed, not caught up\n", job.ID)
	}
	return nil
}

func (b *launchdBackend) Unregister(job Job) error {
	plist := b.plistPath(job.ID)
	execCommand("launchctl", "bootout",
		fmt.Sprintf("gui/%d/%s", os.Getuid(), label(job.ID))).Run()
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// generate renders the job's plist. Pure: no filesystem, no launchctl.
func (b *launchdBackend) generate(job Job, execPath string) ([]byte, error) {
	st := NewStore(b.dir)
	kvs := []plistKV{
		{"Label", pstr(label(job.ID))},
		{"ProgramArguments", plistValue{list: []plistValue{
			pstr(execPath), pstr("-schedule-exec"), pstr(job.ID),
		}}},
		{"RunAtLoad", pbool(false)},
		{"StandardOutPath", pstr(st.OutLogPath(job.ID))},
		{"StandardErrorPath", pstr(st.ErrLogPath(job.ID))},
		{"EnvironmentVariables", pmap(jobEnvironment())},
		{"WorkingDirectory", pstr(job.Workdir)},
	}
	switch job.Kind {
	case KindCron:
		s, err := Parse(job.Cron)
		if err != nil {
			return nil, err
		}
		intervals, err := calendarIntervals(s)
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", job.ID, err)
		}
		kvs = append(kvs, plistKV{"StartCalendarInterval", plistValue{list: intervals}})
	case KindEvery:
		kvs = append(kvs, plistKV{"StartInterval", pint(int(job.EverySec))})
	case KindAt:
		at, err := time.Parse(time.RFC3339, job.At)
		if err != nil {
			return nil, fmt.Errorf("job %s: bad at time: %w", job.ID, err)
		}
		kvs = append(kvs, plistKV{"StartCalendarInterval", plistValue{list: []plistValue{
			pdict(
				plistKV{"Month", pint(int(at.Month()))},
				plistKV{"Day", pint(at.Day())},
				plistKV{"Hour", pint(at.Hour())},
				plistKV{"Minute", pint(at.Minute())},
			),
		}}})
	default:
		return nil, fmt.Errorf("job %s: unknown kind %q", job.ID, job.Kind)
	}
	return marshalPlist(kvs)
}

// maxCalendarIntervals bounds the StartCalendarInterval expansion. launchd
// takes an array of dicts and cron fields combine multiplicatively, so a dense
// expression (`*/2 * * * *` is 30 dicts) explodes fast; past this the plist is
// bigger than the job is worth, and the user should narrow the expression.
const maxCalendarIntervals = 500

// calendarIntervals expands a cron schedule to StartCalendarInterval entries.
// A star field becomes an absent key (launchd's wildcard); restricted fields
// become the cartesian product of their values, one dict per combination.
// Day and Weekday stay in the same dict: launchd ORs exactly those two keys,
// which is cron's own rule, so the OR semantics map one-to-one.
func calendarIntervals(s *Schedule) ([]plistValue, error) {
	fields := []struct {
		key  string
		vals []int // nil = star = key omitted
	}{
		{"Month", setBits(s.Month, 1, 12)},
		{"Day", setBits(s.Dom, 1, 31)},
		{"Weekday", setBits(s.Dow, 0, 6)},
		{"Hour", setBits(s.Hour, 0, 23)},
		{"Minute", setBits(s.Minute, 0, 59)},
	}
	dicts := [][]plistKV{{}}
	for _, f := range fields {
		if f.vals == nil {
			continue
		}
		var next [][]plistKV
		for _, d := range dicts {
			for _, v := range f.vals {
				nd := make([]plistKV, len(d), len(d)+1)
				copy(nd, d)
				nd = append(nd, plistKV{f.key, pint(v)})
				next = append(next, nd)
			}
		}
		dicts = next
		if len(dicts) > maxCalendarIntervals {
			return nil, fmt.Errorf("cron expression expands to more than %d calendar entries; narrow it", maxCalendarIntervals)
		}
	}
	out := make([]plistValue, len(dicts))
	for i, d := range dicts {
		out[i] = pdict(d...)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// plist serialisation (encoding/xml, never string concatenation: the values
// carry prompts, paths and keys, all of which can contain markup)
// ---------------------------------------------------------------------------

type plistKV struct {
	key string
	val plistValue
}

type plistValue struct {
	str  *string
	num  *int
	b    *bool
	list []plistValue
	dict []plistKV
}

func pstr(s string) plistValue { return plistValue{str: &s} }
func pint(n int) plistValue    { return plistValue{num: &n} }
func pbool(v bool) plistValue  { return plistValue{b: &v} }
func pdict(kvs ...plistKV) plistValue {
	if kvs == nil {
		kvs = []plistKV{}
	}
	return plistValue{dict: kvs}
}

func pmap(m map[string]string) plistValue {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kvs := make([]plistKV, 0, len(keys))
	for _, k := range keys {
		kvs = append(kvs, plistKV{k, pstr(m[k])})
	}
	return pdict(kvs...)
}

func (v plistValue) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	switch {
	case v.str != nil:
		return e.EncodeElement(*v.str, xml.StartElement{Name: xml.Name{Local: "string"}})
	case v.num != nil:
		return e.EncodeElement(strconv.Itoa(*v.num), xml.StartElement{Name: xml.Name{Local: "integer"}})
	case v.b != nil:
		name := "false"
		if *v.b {
			name = "true"
		}
		st := xml.StartElement{Name: xml.Name{Local: name}}
		if err := e.EncodeToken(st); err != nil {
			return err
		}
		return e.EncodeToken(st.End())
	case v.dict != nil:
		start.Name.Local = "dict"
		if err := e.EncodeToken(start); err != nil {
			return err
		}
		for _, kv := range v.dict {
			if err := e.EncodeElement(kv.key, xml.StartElement{Name: xml.Name{Local: "key"}}); err != nil {
				return err
			}
			if err := kv.val.MarshalXML(e, xml.StartElement{}); err != nil {
				return err
			}
		}
		return e.EncodeToken(start.End())
	default:
		start.Name.Local = "array"
		if err := e.EncodeToken(start); err != nil {
			return err
		}
		for _, item := range v.list {
			if err := item.MarshalXML(e, xml.StartElement{}); err != nil {
				return err
			}
		}
		return e.EncodeToken(start.End())
	}
}

func marshalPlist(kvs []plistKV) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	buf.WriteString("<plist version=\"1.0\">\n")
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "\t")
	if err := pdict(kvs...).MarshalXML(enc, xml.StartElement{}); err != nil {
		return nil, err
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	buf.WriteString("</plist>\n")
	// encoding/xml cannot emit empty-element tags, but the plist DTD declares
	// true/false as EMPTY and launchd's parser rejects <false></false>
	// (bootstrap fails with error 109 "Invalid property list"), even though
	// plutil -lint accepts it. Rewrite booleans to the self-closing form.
	out := bytes.ReplaceAll(buf.Bytes(), []byte("<true></true>"), []byte("<true/>"))
	out = bytes.ReplaceAll(out, []byte("<false></false>"), []byte("<false/>"))
	return out, nil
}

// ---------------------------------------------------------------------------
// systemd
// ---------------------------------------------------------------------------

// systemdUserDir is a variable for the same reason launchAgentsDir is.
var systemdUserDir = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

type systemdBackend struct {
	dir  string
	warn io.Writer
}

func (b *systemdBackend) Name() string { return "systemd" }

func (b *systemdBackend) paths(id string) (service, timer string) {
	dir := systemdUserDir()
	return filepath.Join(dir, label(id)+".service"), filepath.Join(dir, label(id)+".timer")
}

func (b *systemdBackend) Register(job Job, execPath string) error {
	service, timer, err := b.generate(job, execPath)
	if err != nil {
		return err
	}
	dir := systemdUserDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	svcPath, tmrPath := b.paths(job.ID)
	if err := os.WriteFile(svcPath, []byte(service), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(tmrPath, []byte(timer), 0o600); err != nil {
		return err
	}
	// The service appends to the state logs; systemd does not create parent
	// directories for StandardOutput=append:, so make them here.
	if err := os.MkdirAll(filepath.Join(b.dir, "logs"), 0o700); err != nil {
		return err
	}
	if out, err := execCommand("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %v: %s", err, out)
	}
	unit := label(job.ID) + ".timer"
	if out, err := execCommand("systemctl", "--user", "enable", "--now", unit).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable --now %s: %v: %s", unit, err, out)
	}
	// A timer only runs while a user manager exists, and the manager exits at
	// last logout unless lingering is on — which makes "every night at 3am"
	// silently never happen on a server the user logs into only to deploy.
	if out, err := execCommand("loginctl", "show-user", os.Getenv("USER"), "-p", "Linger").Output(); err == nil {
		if strings.TrimSpace(string(out)) == "Linger=no" {
			fmt.Fprintf(b.warn, "warning: job %s: lingering is off for %s; timers stop at last logout. Run: loginctl enable-linger %s\n",
				job.ID, os.Getenv("USER"), os.Getenv("USER"))
		}
	}
	return nil
}

func (b *systemdBackend) Unregister(job Job) error {
	unit := label(job.ID) + ".timer"
	execCommand("systemctl", "--user", "disable", "--now", unit).Run()
	svcPath, tmrPath := b.paths(job.ID)
	err1 := os.Remove(svcPath)
	err2 := os.Remove(tmrPath)
	execCommand("systemctl", "--user", "daemon-reload").Run()
	for _, err := range []error{err1, err2} {
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// generate renders the service and timer unit texts. Pure.
func (b *systemdBackend) generate(job Job, execPath string) (service, timer string, err error) {
	st := NewStore(b.dir)
	var svc strings.Builder
	svc.WriteString("[Unit]\nDescription=pogo scheduled job " + job.ID + "\n\n[Service]\nType=oneshot\n")
	svc.WriteString("WorkingDirectory=" + job.Workdir + "\n")
	// append: lands the child's output in the same logs launchd writes; needs
	// systemd >= 240 (2018). On older systemd the unit fails to start and the
	// output would have been in the journal instead.
	svc.WriteString("StandardOutput=append:" + st.OutLogPath(job.ID) + "\n")
	svc.WriteString("StandardError=append:" + st.ErrLogPath(job.ID) + "\n")
	env := jobEnvironment()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// %% escapes systemd's specifier expansion; the quotes keep spaces in
		// PATH-like values intact.
		v := strings.ReplaceAll(env[k], "%", "%%")
		fmt.Fprintf(&svc, "Environment=\"%s=%s\"\n", k, v)
	}
	svc.WriteString("ExecStart=" + execPath + " -schedule-exec " + job.ID + "\n")
	svc.WriteString("NoNewPrivileges=yes\n")

	var tmr strings.Builder
	tmr.WriteString("[Unit]\nDescription=pogo scheduled job " + job.ID + " timer\n\n[Timer]\n")
	switch job.Kind {
	case KindCron:
		s, err := Parse(job.Cron)
		if err != nil {
			return "", "", err
		}
		lines, err := cronToOnCalendar(s)
		if err != nil {
			return "", "", fmt.Errorf("job %s: %w", job.ID, err)
		}
		for _, line := range lines {
			tmr.WriteString("OnCalendar=" + line + "\n")
		}
		// Persistent fires once at boot for anything missed while off — the
		// systemd answer to "the laptop was asleep at 9am".
		tmr.WriteString("Persistent=true\n")
	case KindEvery:
		tmr.WriteString("OnUnitActiveSec=" + strconv.FormatInt(job.EverySec, 10) + "s\n")
	case KindAt:
		at, err := time.Parse(time.RFC3339, job.At)
		if err != nil {
			return "", "", fmt.Errorf("job %s: bad at time: %w", job.ID, err)
		}
		tmr.WriteString("OnCalendar=" + at.Format("2006-01-02 15:04:05") + "\n")
	default:
		return "", "", fmt.Errorf("job %s: unknown kind %q", job.ID, job.Kind)
	}
	tmr.WriteString("\n[Install]\nWantedBy=timers.target\n")
	return svc.String(), tmr.String(), nil
}

// cronToOnCalendar converts a schedule to systemd calendar expressions.
//
// systemd ANDs the day-of-week prefix with the date, where cron ORs DOM with
// DOW — so an expression restricting both splits into two OnCalendar lines
// (timer lines are themselves OR'd): one carrying only the day-of-month, one
// carrying only the weekday.
func cronToOnCalendar(s *Schedule) ([]string, error) {
	months := onCalendarComponent(s.Month, 1, 12, nil)
	doms := onCalendarComponent(s.Dom, 1, 31, nil)
	hours := onCalendarComponent(s.Hour, 0, 23, nil)
	mins := onCalendarComponent(s.Minute, 0, 59, nil)
	dows := onCalendarComponent(s.Dow, 0, 6, []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"})
	if months == "" || doms == "" || hours == "" || mins == "" {
		return nil, fmt.Errorf("empty field in cron expression")
	}
	date := "*-" + months + "-" + doms
	tm := hours + ":" + mins + ":00"
	domStar := s.Dom&starBit != 0
	dowStar := s.Dow&starBit != 0
	switch {
	case !domStar && !dowStar:
		return []string{
			date + " " + tm,
			dows + " *-" + months + "-* " + tm,
		}, nil
	case !dowStar:
		// A restricted DOW becomes the weekday prefix; a starred one is
		// already every day and adds nothing.
		return []string{dows + " " + date + " " + tm}, nil
	default:
		return []string{date + " " + tm}, nil
	}
}

// onCalendarComponent renders one field as systemd's `*`, a value, or a
// comma list with `..` ranges. "All values" is the full bitmap, not starBit
// (`*/15` is not `*`); see fieldList.
func onCalendarComponent(bits uint64, lo, hi int, names []string) string {
	vals := setBits(bits, lo, hi)
	if vals == nil {
		return "*"
	}
	if len(vals) == 0 {
		return ""
	}
	var parts []string
	for i := 0; i < len(vals); {
		j := i
		for j+1 < len(vals) && vals[j+1] == vals[j]+1 {
			j++
		}
		render := func(v int) string {
			if names != nil {
				return names[v]
			}
			return strconv.Itoa(v)
		}
		if j > i {
			parts = append(parts, render(vals[i])+".."+render(vals[j]))
		} else {
			parts = append(parts, render(vals[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// ---------------------------------------------------------------------------
// crontab fallback
// ---------------------------------------------------------------------------

type cronBackend struct {
	dir  string
	warn io.Writer
}

func (b *cronBackend) Name() string { return "cron" }

func (b *cronBackend) Register(job Job, execPath string) error {
	st := NewStore(b.dir)
	line, err := crontabLine(job, execPath, st.OutLogPath(job.ID))
	if err != nil {
		return fmt.Errorf("job %s: %w", job.ID, err)
	}
	// The shell redirect creates the log file but not its parent directory.
	if err := os.MkdirAll(filepath.Join(b.dir, "logs"), 0o700); err != nil {
		return err
	}
	existing, err := b.read()
	if err != nil {
		return err
	}
	block := "# BEGIN " + label(job.ID) + "\n" + line + "\n# END " + label(job.ID) + "\n"
	content := stripCrontabBlock(existing, job.ID)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += block
	if err := b.write(content); err != nil {
		return err
	}
	fmt.Fprintf(b.warn, "warning: job %s: cron runs nothing while the machine is off or asleep, and missed runs are not caught up\n", job.ID)
	return nil
}

func (b *cronBackend) Unregister(job Job) error {
	existing, err := b.read()
	if err != nil {
		return err
	}
	content := stripCrontabBlock(existing, job.ID)
	return b.write(content)
}

func (b *cronBackend) read() (string, error) {
	out, err := execCommand("crontab", "-l").Output()
	if err != nil {
		// "No crontab for user" exits non-zero on both BSD and ISC cron;
		// there is no reliable exit-code split from a real failure, so a
		// failed read is treated as an empty crontab. Register then creates
		// one containing only our block, which is the right outcome either way.
		return "", nil
	}
	return string(out), nil
}

func (b *cronBackend) write(content string) error {
	cmd := execCommand("crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab -: %v: %s", err, out)
	}
	return nil
}

// crontabLine renders the job as one crontab entry. Pure.
//
// every-jobs degrade to the nearest minute step cron can say, and at-jobs to
// month/day/hour/minute with no year (cron cannot say "once"): the executor
// disables an at-job after it fires, and the stale line triggers a process
// that exits immediately — noted in the README rather than made perfect.
func crontabLine(job Job, execPath, logPath string) (string, error) {
	var sched string
	switch job.Kind {
	case KindCron:
		s, err := Parse(job.Cron)
		if err != nil {
			return "", err
		}
		sched = s.Standard()
	case KindEvery:
		if job.EverySec < 60 {
			return "", fmt.Errorf("the cron backend cannot run jobs more often than once a minute (want every %ds)", job.EverySec)
		}
		m := job.EverySec / 60
		switch {
		case m < 60:
			sched = fmt.Sprintf("*/%d * * * *", m)
		case m%60 == 0 && m/60 < 24:
			sched = fmt.Sprintf("0 */%d * * *", m/60)
		case m == 60*24:
			sched = "0 0 * * *"
		default:
			return "", fmt.Errorf("the cron backend cannot express an interval of %d seconds; use --cron", job.EverySec)
		}
	case KindAt:
		at, err := time.Parse(time.RFC3339, job.At)
		if err != nil {
			return "", fmt.Errorf("bad at time: %w", err)
		}
		sched = fmt.Sprintf("%d %d %d %d *", at.Minute(), at.Hour(), at.Day(), int(at.Month()))
	default:
		return "", fmt.Errorf("unknown kind %q", job.Kind)
	}
	return fmt.Sprintf("%s cd %s && %s -schedule-exec %s >> %s 2>&1",
		sched, shellQuote(job.Workdir), shellQuote(execPath), shellQuote(job.ID), shellQuote(logPath)), nil
}

// stripCrontabBlock removes the marker block for id from a crontab's text.
func stripCrontabBlock(content, id string) string {
	begin := "# BEGIN " + label(id)
	end := "# END " + label(id)
	var kept []string
	inBlock := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == begin:
			inBlock = true
		case trimmed == end:
			inBlock = false
		case !inBlock:
			kept = append(kept, line)
		}
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n") + "\n"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
