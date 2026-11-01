package session

// This opt-in baseline exercises the production storage functions. It neither
// disables fsync nor changes the store's writer. No production directory is used.
//
// POGO_SESSION_PERF_MODE=smoke validates the harness without publishing timings.
// POGO_SESSION_PERF_MODE=measure records nine independent samples at 10/100/500
// synthetic exchanges. Set POGO_SESSION_PERF_REPORT to a NEW JSON output path.
// Run alone: go test ./session -run '^TestSessionPerformanceBaseline$' -count=1 -v

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/llm"
)

type perfStorageSample struct {
	Sample       int     `json:"sample"`
	ElapsedMS    float64 `json:"elapsed_ms"`
	Allocated    uint64  `json:"allocated_bytes"`
	Allocations  uint64  `json:"allocations"`
	GCs          uint32  `json:"gc_cycles"`
	WrittenBytes int64   `json:"transcript_bytes_added"`
}

type perfStorageMetric struct {
	Name       string              `json:"name"`
	Boundary   string              `json:"boundary"`
	Functional bool                `json:"functional_validation_passed"`
	Samples    []perfStorageSample `json:"samples,omitempty"`
	Statistics map[string]float64  `json:"statistics,omitempty"`
}

type perfStorageScale struct {
	Rounds          int                 `json:"synthetic_exchanges"`
	Messages        int                 `json:"messages"`
	Records         int                 `json:"records"`
	TranscriptBytes int64               `json:"transcript_bytes"`
	PayloadBytes    int                 `json:"append_message_json_bytes"`
	IndexBytes      int64               `json:"index_main_file_bytes"`
	IndexedBytes    int                 `json:"indexed_text_bytes"`
	IndexedRunes    int                 `json:"indexed_text_runes"`
	SQLite          map[string]any      `json:"observed_sqlite"`
	Metrics         []perfStorageMetric `json:"metrics"`
}

type perfStorageReport struct {
	Schema          int                `json:"schema_version"`
	Mode            string             `json:"mode"`
	Measured        bool               `json:"performance_measurements"`
	Passed          bool               `json:"passed"`
	Started         string             `json:"started_at"`
	Finished        string             `json:"finished_at"`
	Environment     map[string]any     `json:"environment"`
	Methodology     map[string]any     `json:"methodology"`
	SourceHashes    map[string]string  `json:"source_sha256"`
	Scales          []perfStorageScale `json:"scales"`
	Limitations     []string           `json:"limitations"`
	Recommendations []string           `json:"recommendations"`
}

func TestSessionPerformanceBaseline(t *testing.T) {
	mode := os.Getenv("POGO_SESSION_PERF_MODE")
	if mode == "" {
		t.Skip("opt-in storage baseline; set POGO_SESSION_PERF_MODE=smoke or measure")
	}
	if mode != "smoke" && mode != "measure" {
		t.Fatal("POGO_SESSION_PERF_MODE must be smoke or measure")
	}
	out := os.Getenv("POGO_SESSION_PERF_REPORT")
	if out == "" {
		t.Fatal("POGO_SESSION_PERF_REPORT must name a new output file")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("report path already exists or cannot be checked; preserve earlier evidence")
	}
	// t.Setenv excludes parallel tests and also prevents an inherited index path
	// from directing any operation into a real session directory.
	t.Setenv("POGO_SESSION_INDEX", "off")
	t.Setenv("POGO_SESSION_DIR", t.TempDir())
	root := t.TempDir()
	rounds, samples, warmups := []int{10, 100, 500}, 9, 1
	if mode == "smoke" {
		rounds, samples, warmups = []int{2}, 1, 0
	}
	r := perfStorageReport{
		Schema: 1, Mode: mode, Measured: mode == "measure", Started: time.Now().UTC().Format(time.RFC3339Nano),
		Environment: perfStorageEnvironment(root), SourceHashes: perfStorageSourceHashes(t),
		Methodology: map[string]any{
			"data_origin": "deterministic synthetic data only; no user sessions or credentials",
			"data_root":   root, "temporary_data_cleanup": "testing.T.TempDir removes all datasets when the test exits",
			"independent_samples_per_metric": samples, "untimed_warmups_per_metric": warmups,
			"operations_per_sample": 1, "operation_order": "rotate metric order by repetition to reduce fixed-order bias; no parallel execution",
			"fixture":               "each exchange has user text 256 runes, assistant tool-use, user tool-result 1024 runes, assistant answer 2048 runes; mixed Chinese/ASCII deterministic unique identifiers",
			"fixture_setup":         "Create and AppendAll through production APIs with derived index off; one batched historical seed outside timing; every sample receives an independently reopened byte-identical transcript copy",
			"append_payload":        "one new 2048-rune assistant answer via real Store.Append; includes record IDs, JSON encode, open/stat/write/Sync/close, memory update, and optional real touchIndex",
			"durability":            "production os.File.Sync is retained in both append variants; no writeFile injection; no directory or device power-loss durability claim",
			"index_off_control":     "only the existing POGO_SESSION_INDEX=off switch; changes derived cache work, never transcript fsync; not a product optimization or speedup claim",
			"cache_state":           "warm OS cache; reopened stores and fresh prepared indexes; component FTS transaction uses a pre-opened initialized connection; no OS cache flush",
			"statistics":            "milliseconds; raw samples, min, max, arithmetic mean, sample standard deviation, nearest-rank p50 and p95; nine samples is exploratory, not a production SLO estimate",
			"allocation_statistics": "runtime.MemStats TotalAlloc/Mallocs/NumGC deltas around each operation; process-wide counters, no forced GC",
			"validation":            "reopen and compare actual message content, verify direct SQLite sessions row size/text and direct FTS hit, verify indexed search snippets; validation outside measured intervals",
		},
		Limitations: []string{
			"Controlled local-storage baseline, not Docker-volume or production-load acceptance.",
			"UI benchmark is coordinated to run separately. Other background applications are not controlled; an idle desktop-browser PoC page remains open and may receive optional physical input.",
			"Synthetic content and warm caches do not represent all user transcripts, branch patterns, I/O contention or cold startup.",
			"Historical seed is batched outside timing; measured append is a single assistant message, not an entire agent exchange or model request.",
			"Component timings overlap conceptually and have different connection/setup boundaries; they must not be summed or subtracted as exact causal attribution.",
			"The existing searchable-text cap is retained; report indexed bytes/runes to show its effect at large histories.",
			"No product logic is changed, fsync is never disabled, and no Rust speedup or production bottleneck is established by this harness.",
		},
		Recommendations: []string{},
	}
	for _, n := range rounds {
		r.Scales = append(r.Scales, perfStorageRunScale(t, root, n, samples, warmups, mode == "measure"))
	}
	for name, hash := range perfStorageSourceHashes(t) {
		if r.SourceHashes[name] != hash {
			t.Fatalf("source changed during baseline: %s", name)
		}
	}
	r.Passed = true
	r.Finished = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s validation passed: %d scale(s), %d sample(s) per metric; report=%s", mode, len(rounds), samples, out)
}

func perfStorageRunScale(t *testing.T, root string, rounds, samples, warmups int, measured bool) perfStorageScale {
	t.Helper()
	baseDir := filepath.Join(root, fmt.Sprintf("rounds-%d", rounds), "seed")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POGO_SESSION_INDEX", "off")
	s, err := Create(baseDir, "/synthetic/performance", "synthetic-no-model")
	if err != nil {
		t.Fatal(err)
	}
	var messages []llm.Message
	for i := 0; i < rounds; i++ {
		messages = append(messages, perfStorageExchange(i)...)
	}
	if err := s.AppendAll(messages); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	sum := summariseRecords(s.Path(), s.records, s.head, s.parent, st.ModTime().UnixMilli())
	payload := llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: perfStorageText(rounds+1, "append-answer", 2048)}}}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	scale := perfStorageScale{Rounds: rounds, Messages: len(messages), Records: len(s.records), TranscriptBytes: st.Size(), PayloadBytes: len(payloadJSON), IndexedBytes: len(sum.text), IndexedRunes: utf8.RuneCountInString(sum.text)}
	scale.Metrics = []perfStorageMetric{
		{Name: "append_one_with_index", Boundary: "Store.Append including real transcript Sync and full synchronous touchIndex"},
		{Name: "append_one_durable_no_index", Boundary: "Store.Append with only POGO_SESSION_INDEX=off; transcript Sync remains enabled"},
		{Name: "summary_in_memory", Boundary: "existing summariseRecords over already-loaded records; includes full position-map/branch walk and capped searchable text"},
		{Name: "touch_index_full", Boundary: "existing Store.touchIndex including stat, summary, withIndex open/schema check, transaction/FTS replacement and close"},
		{Name: "fts_replace_transaction", Boundary: "existing replaceFTS inside real Begin/Commit on an initialized connection with session row present; excludes connection open, summary and row upsert"},
		{Name: "search_indexed_with_snippets", Boundary: "existing withIndex plus searchIndexed including open/schema check, FTS query, transcript-backed snippet extraction and close; index failure is fatal, never scan fallback"},
	}
	for repetition := -warmups; repetition < samples; repetition++ {
		for offset := range scale.Metrics {
			metric := (offset + repetition + warmups) % len(scale.Metrics)
			m := &scale.Metrics[metric]
			dir := filepath.Join(filepath.Dir(baseDir), fmt.Sprintf("sample-%d-metric-%d", repetition+warmups, metric))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, filepath.Base(s.Path()))
			if err := os.WriteFile(path, seed, 0o600); err != nil {
				t.Fatal(err)
			}
			copyFile, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := copyFile.Sync(); err != nil {
				copyFile.Close()
				t.Fatal(err)
			}
			if err := copyFile.Close(); err != nil {
				t.Fatal(err)
			}
			copyStore, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(copyStore.Diagnostics()) != 0 {
				t.Fatal(copyStore.Diagnostics())
			}
			t.Setenv("POGO_SESSION_INDEX", "")
			copyStore.touchIndex()
			perfStorageValidateIndex(t, copyStore, rounds)
			if scale.SQLite == nil {
				scale.SQLite = perfStorageSQLiteSettings(t, dir)
				indexStat, err := os.Stat(filepath.Join(dir, IndexName))
				if err != nil {
					t.Fatal(err)
				}
				scale.IndexBytes = indexStat.Size()
			}
			var returnedSummary summary
			var hits []Hit
			var sample perfStorageSample
			measure := func(fn func() error) { sample = perfStorageMeasure(t, measured && repetition >= 0, fn) }
			switch metric {
			case 0:
				measure(func() error { return copyStore.Append(payload) })
			case 1:
				t.Setenv("POGO_SESSION_INDEX", "off")
				measure(func() error { return copyStore.Append(payload) })
			case 2:
				measure(func() error {
					returnedSummary = summariseRecords(copyStore.path, copyStore.records, copyStore.head, copyStore.parent, st.ModTime().UnixMilli())
					return nil
				})
			case 3:
				measure(func() error { copyStore.touchIndex(); return nil })
			case 4:
				ok := withIndex(dir, func(db *sql.DB) error {
					localSum := summariseRecords(copyStore.path, copyStore.records, copyStore.head, copyStore.parent, st.ModTime().UnixMilli())
					measure(func() error {
						tx, err := db.Begin()
						if err != nil {
							return err
						}
						defer tx.Rollback()
						if err := replaceFTS(tx, localSum); err != nil {
							return err
						}
						return tx.Commit()
					})
					return nil
				})
				if !ok {
					t.Fatal("FTS measurement failed to open real index")
				}
			case 5:
				measure(func() error {
					var queryErr error
					ok := withIndex(dir, func(db *sql.DB) error { hits, queryErr = searchIndexed(db, dir, "PerfBodyAnchor", 20); return queryErr })
					if !ok {
						return fmt.Errorf("indexed search failed without fallback: %v", queryErr)
					}
					return nil
				})
			}
			if metric < 2 {
				reopened, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				actual := reopened.Messages("")
				if len(actual) != len(messages)+1 || actual[len(actual)-1].Text() != payload.Text() || len(reopened.Diagnostics()) != 0 {
					t.Fatal("real append did not survive reopen")
				}
				newStat, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				sample.WrittenBytes = newStat.Size() - int64(len(seed))
			}
			if metric == 2 && (returnedSummary.text != sum.text || returnedSummary.info.Messages != rounds) {
				t.Fatal("summary differs from fixture")
			}
			if metric == 5 && (len(hits) != 1 || len(hits[0].Blocks) == 0) {
				t.Fatal("indexed query did not return the expected transcript snippet")
			}
			if metric != 1 {
				perfStorageValidateIndex(t, copyStore, rounds)
			}
			m.Functional = true
			if measured && repetition >= 0 {
				sample.Sample = repetition + 1
				m.Samples = append(m.Samples, sample)
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
	}
	if measured {
		for i := range scale.Metrics {
			scale.Metrics[i].Statistics = perfStorageStatistics(scale.Metrics[i].Samples)
		}
	}
	return scale
}

func perfStorageMeasure(t *testing.T, timed bool, fn func() error) perfStorageSample {
	t.Helper()
	if !timed {
		if err := fn(); err != nil {
			t.Fatal(err)
		}
		return perfStorageSample{}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	err := fn()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	return perfStorageSample{ElapsedMS: float64(elapsed.Nanoseconds()) / 1e6, Allocated: after.TotalAlloc - before.TotalAlloc, Allocations: after.Mallocs - before.Mallocs, GCs: after.NumGC - before.NumGC}
}

func perfStorageValidateIndex(t *testing.T, s *Store, rounds int) {
	t.Helper()
	st, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	want := summariseRecords(s.path, s.records, s.head, s.parent, st.ModTime().UnixMilli())
	var validationErr error
	ok := withIndex(filepath.Dir(s.Path()), func(db *sql.DB) error {
		var size int64
		var messages, matches int
		var indexedText string
		sid := strings.TrimSuffix(filepath.Base(s.path), ".jsonl")
		if err := db.QueryRow(`SELECT size, messages, text FROM sessions WHERE sid = ?`, sid).Scan(&size, &messages, &indexedText); err != nil {
			validationErr = err
			return err
		}
		if err := db.QueryRow(`SELECT count(*) FROM sessions_fts WHERE sessions_fts MATCH 'PerfBodyAnchor'`).Scan(&matches); err != nil {
			validationErr = err
			return err
		}
		if size != st.Size() || messages != rounds || indexedText != want.text || matches != 1 {
			validationErr = fmt.Errorf("index state mismatch: size=%d want=%d messages=%d want=%d matches=%d", size, st.Size(), messages, rounds, matches)
			return validationErr
		}
		return nil
	})
	if !ok {
		t.Fatalf("real index validation failed (no scan fallback): %v", validationErr)
	}
}

func perfStorageSQLiteSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	out := map[string]any{}
	if !withIndex(dir, func(db *sql.DB) error {
		for key, query := range map[string]string{"sqlite_version": "SELECT sqlite_version()", "journal_mode": "PRAGMA journal_mode", "synchronous": "PRAGMA synchronous", "busy_timeout": "PRAGMA busy_timeout", "page_size": "PRAGMA page_size"} {
			var value any
			if err := db.QueryRow(query).Scan(&value); err != nil {
				return err
			}
			out[key] = value
		}
		return nil
	}) {
		t.Fatal("cannot observe actual SQLite settings")
	}
	return out
}

func perfStorageExchange(i int) []llm.Message {
	id := fmt.Sprintf("synthetic-tool-%06d", i)
	answer := perfStorageText(i, "answer", 2048)
	if i == 0 {
		answer = "PerfBodyAnchor " + answer
	}
	return []llm.Message{
		llm.UserText(perfStorageText(i, "prompt", 256)),
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: "read", Input: json.RawMessage(`{"path":"synthetic.txt"}`)}}},
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: id, Text: perfStorageText(i, "result", 1024)}}},
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: answer}}},
	}
}

func perfStorageText(round int, kind string, n int) string {
	var b strings.Builder
	for word := 0; b.Len() < n*2; word++ {
		fmt.Fprintf(&b, "性能基线 %s round%d segment%d storage_index_%08x durable text. ", kind, round, word, uint32(round*7919+word*104729))
	}
	r := []rune(b.String())
	if len(r) < n {
		panic("synthetic text generation underflow")
	}
	return string(r[:n])
}

func perfStorageStatistics(samples []perfStorageSample) map[string]float64 {
	values := make([]float64, len(samples))
	var total, allocated float64
	for i, sample := range samples {
		values[i] = sample.ElapsedMS
		total += sample.ElapsedMS
		allocated += float64(sample.Allocated)
	}
	sort.Float64s(values)
	mean := total / float64(len(values))
	var ss float64
	for _, value := range values {
		ss += (value - mean) * (value - mean)
	}
	stdev := 0.0
	if len(values) > 1 {
		stdev = math.Sqrt(ss / float64(len(values)-1))
	}
	return map[string]float64{"n": float64(len(values)), "min_ms": values[0], "max_ms": values[len(values)-1], "mean_ms": mean, "sample_stddev_ms": stdev, "p50_ms": values[int(math.Ceil(.5*float64(len(values))))-1], "p95_ms": values[int(math.Ceil(.95*float64(len(values))))-1], "mean_allocated_bytes": allocated / float64(len(values))}
}

func perfStorageEnvironment(root string) map[string]any {
	out := map[string]any{"go_version": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH, "logical_cpus": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "temp_root": root, "pid": os.Getpid(), "execution": "local go test process; no model, browser, container or production requests"}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "modernc.org/sqlite" {
				out["modernc_sqlite_module"] = dep.Version
			}
		}
	}
	// Only explicit non-secret machine/storage facts; never dump environment or
	// configuration. Commands are outside all measured intervals.
	commands := map[string][]string{"os_release": {"uname", "-sr"}, "filesystem": {"df", "-T", root}}
	if runtime.GOOS == "darwin" {
		commands = map[string][]string{"os_release": {"sw_vers", "-productVersion"}, "cpu_model": {"sysctl", "-n", "machdep.cpu.brand_string"}, "physical_memory_bytes": {"sysctl", "-n", "hw.memsize"}, "filesystem": {"df", "-T", "apfs", root}}
	}
	for key, args := range commands {
		data, err := exec.Command(args[0], args[1:]...).Output()
		if err == nil {
			out[key] = strings.TrimSpace(string(data))
		} else {
			out[key] = "unavailable"
		}
	}
	return out
}

func perfStorageSourceHashes(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"session.go", "index.go", "session_performance_test.go", "../go.mod", "../go.sum"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		out[name] = hex.EncodeToString(h[:])
	}
	return out
}
