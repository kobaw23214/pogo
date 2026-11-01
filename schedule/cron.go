// Package schedule is pogo's scheduled-task facility: cron parsing, the job
// store, the OS backends (launchd / systemd / crontab) and the executor the OS
// calls back into.
//
// pogo has no daemon, so "run this prompt every morning at nine" has to be
// handed to something that does run while pogo itself is off. That something
// is the OS's own scheduler, and the hand-off is the whole shape of the
// package: `-schedule add` writes the job to ~/.pogo/schedule/jobs.json and
// registers a trigger with the OS; the trigger's only job is to run
// `pogo -schedule-exec <id>`, which does the real work and the bookkeeping.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// starBit marks a field the user wrote as `*`. It lives in the top bit of the
// field's bitmask, outside every field's numeric range, and it exists for one
// reason: Vixie cron's day rule. When DOM and DOW are both restricted the
// match is OR; when either is a star the match is AND. Without a record of
// "the user wrote *", the fully-set bitmap of a star field is indistinguishable
// from a field that listed every value, and the two want different semantics.
const starBit = uint64(1) << 63

// Schedule is a parsed five-field cron expression: minute hour day-of-month
// month day-of-week. Bit n of each mask means "n matches".
//
// The scheme is robfig/cron's: one uint64 per field, bit-set membership tests,
// and the same wrap-and-carry Next algorithm. Reimplemented rather than
// imported because the module's rule is stdlib-only for this feature, and the
// parser is a page of code.
type Schedule struct {
	Minute, Hour, Dom, Month, Dow uint64
	// Raw is the expression as given, after macro expansion.
	Raw string
}

// macros are the @-forms, expanded before parsing. @every is deliberately not
// here: it is a different job kind (KindEvery), not a cron expression.
var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// Parse parses a five-field cron expression or one of the @-macros.
//
// Supported per field: `*`, comma lists, `a-b` ranges, `/step` (including
// `a/step`, which expands to `a-max/step`), and three-letter English names for
// month and weekday (case-insensitive). Day-of-week accepts 0-7, with 7
// normalised to 0 (Sunday).
func Parse(expr string) (*Schedule, error) {
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return nil, fmt.Errorf("empty cron expression")
	}
	if strings.HasPrefix(raw, "@") {
		expanded, ok := macros[strings.ToLower(raw)]
		if !ok {
			return nil, fmt.Errorf("unknown macro %q (known: @yearly @annually @monthly @weekly @daily @midnight @hourly)", raw)
		}
		raw = expanded
	}
	fields := strings.Fields(raw)
	if len(fields) != 5 {
		return nil, fmt.Errorf("expected 5 fields (minute hour day-of-month month day-of-week), got %d: %q", len(fields), expr)
	}
	s := &Schedule{Raw: raw}
	var err error
	if s.Minute, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if s.Hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if s.Dom, err = parseField(fields[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day-of-month: %w", err)
	}
	if s.Month, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if s.Dow, err = parseField(fields[4], 0, 7, dowNames); err != nil {
		return nil, fmt.Errorf("day-of-week: %w", err)
	}
	// Both 0 and 7 are Sunday; fold 7 into 0 so there is one representation.
	if s.Dow&(1<<7) != 0 {
		s.Dow &^= 1 << 7
		s.Dow |= 1
	}
	return s, nil
}

// parseField parses one comma-separated field into a bitmask.
func parseField(field string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		b, err := parseRange(part, lo, hi, names)
		if err != nil {
			return 0, err
		}
		bits |= b
	}
	return bits, nil
}

// parseRange parses one list element: `*`, `a`, `a-b`, each with an optional
// `/step`.
func parseRange(expr string, lo, hi int, names map[string]int) (uint64, error) {
	if expr == "" {
		return 0, fmt.Errorf("empty list element")
	}
	rangeAndStep := strings.Split(expr, "/")
	if len(rangeAndStep) > 2 {
		return 0, fmt.Errorf("too many slashes in %q", expr)
	}
	lowAndHigh := strings.Split(rangeAndStep[0], "-")
	if len(lowAndHigh) > 2 {
		return 0, fmt.Errorf("too many dashes in %q", expr)
	}

	var start, end int
	var star bool
	single := len(lowAndHigh) == 1
	switch {
	case lowAndHigh[0] == "*":
		if !single {
			return 0, fmt.Errorf("'*' cannot be part of a range in %q", expr)
		}
		start, end, star = lo, hi, true
	case single:
		v, err := parseIntOrName(lowAndHigh[0], names)
		if err != nil {
			return 0, err
		}
		start, end = v, v
	default:
		s, err := parseIntOrName(lowAndHigh[0], names)
		if err != nil {
			return 0, err
		}
		e, err := parseIntOrName(lowAndHigh[1], names)
		if err != nil {
			return 0, err
		}
		start, end = s, e
	}

	step := 1
	if len(rangeAndStep) == 2 {
		v, err := strconv.Atoi(rangeAndStep[1])
		if err != nil || v <= 0 {
			return 0, fmt.Errorf("invalid step %q in %q: must be a positive integer", rangeAndStep[1], expr)
		}
		step = v
		// A single number with a step means "from N to the top of the range":
		// `5/20` in the minute field is 5, 25, 45.
		if single && !star {
			end = hi
		}
	}
	if start < lo || end > hi {
		return 0, fmt.Errorf("%q is out of range [%d,%d]", expr, lo, hi)
	}
	if start > end {
		return 0, fmt.Errorf("range start %d is greater than end %d in %q", start, end, expr)
	}

	var bits uint64
	for i := start; i <= end; i += step {
		bits |= uint64(1) << uint(i)
	}
	if star {
		bits |= starBit
	}
	return bits, nil
}

func parseIntOrName(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number or a known name", s)
	}
	return v, nil
}

// Next returns the first time after t the schedule fires, in t's location.
//
// The algorithm is robfig/cron's spec.go: truncate to the next minute, then
// walk month → day → hour → minute, resetting lower fields on entry and
// wrapping (goto WRAP) whenever a field carries into a higher one, so each
// field is always re-examined against a date the higher fields have already
// accepted. Nothing here accounts for DST transitions: a wall-clock time that
// does not exist on the local clock (spring-forward) is simply never produced
// by time.Date, and the walk moves past it.
//
// The search gives up five years out and returns the zero time — the practical
// meaning of "this expression never fires" (2 月 30 日).
func (s *Schedule) Next(t time.Time) time.Time {
	// The current minute never matches: "next" is strictly after t.
	t = t.Truncate(time.Minute).Add(time.Minute)

	added := false
	yearLimit := t.Year() + 5

WRAP:
	for t.Year() <= yearLimit {
		for uint64(1)<<uint(t.Month())&s.Month == 0 {
			if !added {
				added = true
				t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
			}
			t = t.AddDate(0, 1, 0)
			if t.Month() == time.January {
				goto WRAP
			}
		}

		for !s.dayMatch(t) {
			if !added {
				added = true
				t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			}
			t = t.AddDate(0, 0, 1)
			if t.Day() == 1 {
				goto WRAP
			}
		}

		for uint64(1)<<uint(t.Hour())&s.Hour == 0 {
			if !added {
				added = true
				t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
			}
			t = t.Add(time.Hour)
			if t.Hour() == 0 {
				goto WRAP
			}
		}

		for uint64(1)<<uint(t.Minute())&s.Minute == 0 {
			if !added {
				added = true
				t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, t.Location())
			}
			t = t.Add(time.Minute)
			if t.Minute() == 0 {
				goto WRAP
			}
		}

		return t
	}
	return time.Time{}
}

// dayMatch implements the Vixie day rule: when DOM and DOW are both
// restricted, either matching is enough (OR); when at least one is a star,
// both must match (AND). The star cases collapse to "the restricted field
// decides", which is what everyone expects from `0 9 * * mon`.
func (s *Schedule) dayMatch(t time.Time) bool {
	domMatch := uint64(1)<<uint(t.Day())&s.Dom != 0
	dowMatch := uint64(1)<<uint(t.Weekday())&s.Dow != 0
	if s.Dom&starBit != 0 || s.Dow&starBit != 0 {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}

// Standard renders the schedule back as a plain five-field expression with no
// macros and no names — the form a system crontab is guaranteed to accept.
func (s *Schedule) Standard() string {
	return strings.Join([]string{
		fieldList(s.Minute, 0, 59, nil),
		fieldList(s.Hour, 0, 23, nil),
		fieldList(s.Dom, 1, 31, nil),
		fieldList(s.Month, 1, 12, nil),
		fieldList(s.Dow, 0, 6, nil),
	}, " ")
}

// fieldList renders a bitmask as `*` or a comma list of values (names when a
// table is given).
//
// "All values" is decided by the bitmap itself, not by starBit: `*/15` carries
// starBit (the user typed a star) but matches only four values, and rendering
// it as `*` would silently widen the schedule. starBit's only consumer is the
// DOM/DOW rule in dayMatch.
func fieldList(bits uint64, lo, hi int, names []string) string {
	mask := uint64(0)
	for i := lo; i <= hi; i++ {
		mask |= uint64(1) << uint(i)
	}
	if bits&mask == mask {
		return "*"
	}
	var parts []string
	for i := lo; i <= hi; i++ {
		if bits&(uint64(1)<<uint(i)) == 0 {
			continue
		}
		if names != nil {
			parts = append(parts, names[i])
		} else {
			parts = append(parts, strconv.Itoa(i))
		}
	}
	return strings.Join(parts, ",")
}

// setBits returns the values in [lo, hi] whose bits are set, or nil when every
// value in the range is set — the two cases a backend expands differently.
// Same full-range-not-starBit rule as fieldList.
func setBits(bits uint64, lo, hi int) []int {
	var out []int
	for i := lo; i <= hi; i++ {
		if bits&(uint64(1)<<uint(i)) != 0 {
			out = append(out, i)
		}
	}
	if len(out) == hi-lo+1 {
		return nil
	}
	return out
}
