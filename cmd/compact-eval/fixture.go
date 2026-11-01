package main

import (
	"fmt"
	"strings"
)

// The session's tool output. It is what a real session carries: source files,
// a failing test run, linter noise, a benchmark, a log. The five recall facts
// appear only here and in the assistant's notes; the constraints never do,
// and nothing here hints at them (no amount_cents, no commit prefix
// convention, no vendor/ path).

const ledgerSource = `package billing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Entry is one posted ledger line. AmountMinor is in the currency's minor unit.
type Entry struct {
	ID          string
	AccountID   string
	Currency    string
	AmountMinor int64
	PostedAt    time.Time
	Memo        string
}

// Total is what Totals reports per account. Display is for humans only.
type Total struct {
	AccountID string
	Currency  string
	Minor     int64
	Display   string
}

// Store reads posted entries.
type Store interface {
	Entries(ctx context.Context, accountID string, from, to time.Time) ([]Entry, error)
	Accounts(ctx context.Context) ([]string, error)
}

var ErrNoAccounts = errors.New("ledger: no accounts")

// Ledger aggregates entries from a Store.
type Ledger struct {
	store Store
	now   func() time.Time
}

func New(store Store) *Ledger { return &Ledger{store: store, now: time.Now} }

// Totals sums every entry an account ever posted. internal/legacy/report.go
// depends on its return shape, one Total per account.
func (l *Ledger) Totals(ctx context.Context) ([]Total, error) {
	accounts, err := l.store.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, ErrNoAccounts
	}
	sort.Strings(accounts)
	out := make([]Total, 0, len(accounts))
	for _, account := range accounts {
		entries, err := l.store.Entries(ctx, account, time.Time{}, l.now())
		if err != nil {
			return nil, fmt.Errorf("ledger: entries for %s: %w", account, err)
		}
		var t Total
		t.AccountID = account
		for _, e := range entries {
			if t.Currency == "" {
				t.Currency = e.Currency
			}
			// Mixed currencies are added as if they were one: see E4127.
			t.Minor += e.AmountMinor
		}
		t.Display = display(t.Minor, t.Currency)
		out = append(out, t)
	}
	return out, nil
}

func display(minor int64, currency string) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, minor/100, minor%100, currency)
}
`

const ledgerMain = `package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/acme/billing/internal/billing"
	"github.com/acme/billing/internal/store/pg"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ledger <totals|export> [flags]")
		os.Exit(2)
	}
	ctx := context.Background()
	db, err := pg.Open(os.Getenv("BILLING_DSN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ledger:", err)
		os.Exit(1)
	}
	defer db.Close()
	l := billing.New(db)
	switch os.Args[1] {
	case "totals":
		fs := flag.NewFlagSet("totals", flag.ExitOnError)
		pretty := fs.Bool("pretty", false, "indent output")
		_ = fs.Parse(os.Args[2:])
		totals, err := l.Totals(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ledger:", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		if *pretty {
			enc.SetIndent("", "  ")
		}
		_ = enc.Encode(totals)
	case "export":
		fmt.Fprintln(os.Stderr, "ledger: export is handled by internal/legacy")
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "ledger: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}
`

const grepTotals = `internal/billing/ledger.go:52:func (l *Ledger) Totals(ctx context.Context) ([]Total, error) {
internal/billing/ledger_test.go:31:	totals, err := l.Totals(ctx)
internal/billing/ledger_test.go:58:	totals, err := l.Totals(ctx)
internal/billing/ledger_test.go:97:	_, err := l.Totals(ctx)
internal/legacy/report.go:41:	totals, err := billing.New(store).Totals(ctx)
internal/legacy/report.go:77:	for _, t := range totals { // relies on one Total per account
cmd/ledger/main.go:36:		totals, err := l.Totals(ctx)
internal/api/handlers.go:118:	totals, err := h.ledger.Totals(r.Context())`

const configSource = `package billing

import (
	"os"
	"time"
)

// Location is where month boundaries are computed. BILLING_TZ overrides it;
// the deployment sets BILLING_TZ=Asia/Shanghai.
func Location() *time.Location {
	name := os.Getenv("BILLING_TZ")
	if name == "" {
		name = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// MonthRange returns [start, end) of the month containing t in loc.
func MonthRange(t time.Time, loc *time.Location) (time.Time, time.Time) {
	t = t.In(loc)
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 1, 0)
}
`

const currencySource = `package billing

import "strings"

// Minor-unit exponents for the currencies the ledger accepts.
var exponents = map[string]int{"CNY": 2, "USD": 2, "JPY": 0, "EUR": 2, "HKD": 2}

// Normalize upper-cases a currency code and reports whether it is accepted.
func Normalize(code string) (string, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	_, ok := exponents[code]
	return code, ok
}

// Exponent is the number of minor-unit digits, or -1 for an unknown code.
func Exponent(code string) int {
	if e, ok := exponents[strings.ToUpper(code)]; ok {
		return e
	}
	return -1
}
`

var failingTests = func() string {
	var b strings.Builder
	for i := 1; i <= 24; i++ {
		fmt.Fprintf(&b, "=== RUN   TestEntryValidation/case_%02d\n--- PASS: TestEntryValidation/case_%02d (0.00s)\n", i, i)
	}
	b.WriteString("=== RUN   TestTotalsSingleCurrency\n--- PASS: TestTotalsSingleCurrency (0.01s)\n")
	b.WriteString("=== RUN   TestTotalsMixedCurrency\n    ledger_test.go:97: E4127 ledger: currency mismatch (CNY vs USD) at ledger.go:212\n")
	b.WriteString("    ledger_test.go:98: want error for mixed currencies, got Total{AccountID:\"acct-0042\", Currency:\"CNY\", Minor:129900}\n")
	b.WriteString("--- FAIL: TestTotalsMixedCurrency (0.01s)\n")
	for i := 1; i <= 18; i++ {
		fmt.Fprintf(&b, "=== RUN   TestDisplay/minor_%03d\n--- PASS: TestDisplay/minor_%03d (0.00s)\n", i*37, i*37)
	}
	b.WriteString("FAIL\nFAIL\tgithub.com/acme/billing/internal/billing\t0.118s\nok  \tgithub.com/acme/billing/internal/billing/export\t0.031s\nFAIL\n")
	return b.String()
}()

var vetOutput = func() string {
	var b strings.Builder
	files := []string{"internal/api/handlers.go", "internal/store/pg/query.go", "internal/billing/ledger_test.go", "cmd/ledger/main.go"}
	checks := []string{"SA1019: strings.Title has been deprecated", "ST1005: error strings should not be capitalized", "S1002: should omit comparison to bool constant", "SA4006: a value assigned is never used"}
	for i := 0; i < 96; i++ {
		fmt.Fprintf(&b, "%s:%d:%d: %s (%s)\n", files[i%len(files)], 20+i*7, 2+i%9, checks[i%len(checks)], strings.Fields(checks[i%len(checks)])[0])
	}
	return b.String()
}()

var benchOutput = func() string {
	var b strings.Builder
	b.WriteString("goos: linux\ngoarch: amd64\npkg: github.com/acme/billing/internal/billing\ncpu: Intel(R) Xeon(R) Gold 6348 CPU @ 2.60GHz\n")
	b.WriteString("BenchmarkMonthly-8   \t   65012\t     18432 ns/op\t     412 B/op\t       9 allocs/op\n")
	b.WriteString("BenchmarkDisplay-8   \t 9821773\t       121.8 ns/op\t      16 B/op\t       1 allocs/op\n")
	b.WriteString("PASS\nok  \tgithub.com/acme/billing/internal/billing\t2.913s\n")
	return b.String()
}()

// goFile is plausible package code: n small documented functions over a
// shared receiver, so the summariser has real code to skim, not noise.
func goFile(pkg, receiver, noun string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"context\"\n\t\"fmt\"\n\t\"net/http\"\n\t\"time\"\n)\n\n", pkg)
	verbs := []string{"List", "Get", "Update", "Archive", "Count", "Export", "Validate", "Reconcile"}
	for i := 0; i < n; i++ {
		verb := verbs[i%len(verbs)]
		name := fmt.Sprintf("%s%s%d", verb, noun, i/len(verbs)+1)
		fmt.Fprintf(&b, "// %s handles the %s %s variant %d; it is safe for concurrent use.\n", name, strings.ToLower(verb), strings.ToLower(noun), i)
		fmt.Fprintf(&b, "func (%s) %s(ctx context.Context, id string, at time.Time) (int, error) {\n", receiver, name)
		fmt.Fprintf(&b, "\tif id == \"\" {\n\t\treturn http.StatusBadRequest, fmt.Errorf(\"%s: empty id\")\n\t}\n", strings.ToLower(name))
		fmt.Fprintf(&b, "\tctx, cancel := context.WithTimeout(ctx, %d*time.Second)\n\tdefer cancel()\n", 2+i%5)
		fmt.Fprintf(&b, "\tif at.IsZero() {\n\t\tat = time.Now()\n\t}\n\t_ = ctx\n\treturn http.StatusOK + %d, nil\n}\n\n", i%3)
	}
	return b.String()
}

// verboseTests is a long passing `go test -v` run across packages.
func verboseTests(n int) string {
	var b strings.Builder
	pkgs := []string{"internal/api", "internal/store/pg", "internal/billing/export", "internal/billing"}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Test%sCase%03d", []string{"Handler", "Query", "Export", "Range"}[i%4], i)
		fmt.Fprintf(&b, "=== RUN   %s\n=== PAUSE %s\n=== CONT  %s\n--- PASS: %s (0.0%ds)\n", name, name, name, name, i%9)
		if i%25 == 24 {
			fmt.Fprintf(&b, "ok  \tgithub.com/acme/billing/%s\t0.%03ds\n", pkgs[(i/25)%len(pkgs)], 100+i)
		}
	}
	return b.String()
}

// draftDiff is the diff of a first monthly draft: plausible, long, unfinished.
func draftDiff(n int) string {
	var b strings.Builder
	b.WriteString("diff --git a/internal/billing/monthly.go b/internal/billing/monthly.go\nnew file mode 100644\n--- /dev/null\n+++ b/internal/billing/monthly.go\n@@ -0,0 +1," + fmt.Sprint(n) + " @@\n")
	for i := 0; i < n; i++ {
		switch i % 6 {
		case 0:
			fmt.Fprintf(&b, "+// step %d: group entries by currency before summing\n", i)
		case 1:
			fmt.Fprintf(&b, "+\tgroups[e.Currency] = append(groups[e.Currency], e) // %d\n", i)
		case 2:
			fmt.Fprintf(&b, "+\tif e.PostedAt.Before(start) || !e.PostedAt.Before(end) { continue } // %d\n", i)
		case 3:
			fmt.Fprintf(&b, "+\tsum += e.AmountMinor // %d\n", i)
		case 4:
			fmt.Fprintf(&b, "+\tsort.Slice(rows, func(i, j int) bool { return rows[i].AccountID < rows[j].AccountID }) // %d\n", i)
		default:
			b.WriteString("+\n")
		}
	}
	return b.String()
}

var gitLog = func() string {
	verbs := []string{"store: retry serialization failures", "api: paginate account listing", "export: stream CSV rows",
		"ledger: reject unknown currencies", "deps: bump pgx", "api: add request ids", "store: index entries by posted_at", "docs: describe export format"}
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "%07x %s (#%d)\n", 0x3a1f00b+i*7919, verbs[i%len(verbs)], 900-i)
	}
	return b.String()
}()
