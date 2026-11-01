package schedule

import (
	"strings"
	"testing"
	"time"
)

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestParseAndNext(t *testing.T) {
	cases := []struct {
		expr string
		from time.Time
		want time.Time
	}{
		// Every minute; the current minute never counts, even mid-minute.
		{"* * * * *", at(2026, 8, 22, 10, 30), at(2026, 8, 22, 10, 31)},
		{"* * * * *", time.Date(2026, 8, 22, 10, 30, 45, 0, time.UTC), at(2026, 8, 22, 10, 31)},
		// Step.
		{"*/15 * * * *", at(2026, 8, 22, 10, 30), at(2026, 8, 22, 10, 45)},
		// N/step expands to N-max/step: 5, 25, 45.
		{"5/20 * * * *", at(2026, 8, 22, 10, 0), at(2026, 8, 22, 10, 5)},
		{"5/20 * * * *", at(2026, 8, 22, 10, 6), at(2026, 8, 22, 10, 25)},
		// Fixed time carries into the next day.
		{"0 9 * * *", at(2026, 8, 22, 10, 0), at(2026, 8, 23, 9, 0)},
		{"0 9 * * *", at(2026, 8, 22, 8, 59), at(2026, 8, 22, 9, 0)},
		// Lists and ranges.
		{"0 9,17 * * *", at(2026, 8, 22, 10, 0), at(2026, 8, 22, 17, 0)},
		{"0 9-11 * * *", at(2026, 8, 22, 10, 30), at(2026, 8, 22, 11, 0)},
		{"0 9-11 * * *", at(2026, 8, 22, 11, 30), at(2026, 8, 23, 9, 0)},
		// Names, case-insensitive. January + Monday, AND (dom is a star).
		// 2027-01-04 is the first Monday of January 2027.
		{"0 0 * JAN Mon", at(2026, 8, 22, 0, 0), at(2027, 1, 4, 0, 0)},
		// DOW 7 normalises to 0 (Sunday). 2026-03-01 is a Sunday, so the
		// next one is 03-08.
		{"0 0 * * 7", at(2026, 3, 1, 0, 1), at(2026, 3, 8, 0, 0)},
		{"0 0 * * sun", at(2026, 3, 1, 0, 1), at(2026, 3, 8, 0, 0)},
		// DOM/DOW both restricted is OR (Vixie): 2026-03-06 is a Friday and
		// fires before the 13th does.
		{"0 0 13 * 5", at(2026, 3, 1, 0, 0), at(2026, 3, 6, 0, 0)},
		// With DOW a star the AND collapses to DOM alone: the 13th.
		{"0 0 13 * *", at(2026, 3, 1, 0, 0), at(2026, 3, 13, 0, 0)},
		// The exact boundary minute does not match; the next firing does.
		{"30 9 * * *", time.Date(2026, 8, 22, 9, 30, 45, 0, time.UTC), at(2026, 8, 23, 9, 30)},
		// Leap year: Feb 29 from a non-leap year carries across years.
		{"0 0 29 2 *", at(2026, 1, 1, 0, 0), at(2028, 2, 29, 0, 0)},
		// Macros.
		{"@daily", at(2026, 8, 22, 10, 30), at(2026, 8, 23, 0, 0)},
		{"@midnight", at(2026, 8, 22, 10, 30), at(2026, 8, 23, 0, 0)},
		{"@hourly", at(2026, 8, 22, 10, 30), at(2026, 8, 22, 11, 0)},
		// 2026-08-22 is a Saturday; @weekly fires Sunday 00:00.
		{"@weekly", at(2026, 8, 22, 0, 0), at(2026, 8, 23, 0, 0)},
		{"@monthly", at(2026, 8, 22, 0, 0), at(2026, 9, 1, 0, 0)},
		{"@yearly", at(2026, 8, 22, 0, 0), at(2027, 1, 1, 0, 0)},
		// Hour wrap re-enters the day check: 2026-03-31 fires only if it
		// matches the day fields.
		{"0 23 31 3 *", at(2026, 3, 31, 22, 0), at(2026, 3, 31, 23, 0)},
	}
	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.expr, err)
			continue
		}
		if got := s.Next(c.from); !got.Equal(c.want) {
			t.Errorf("Next(%q from %s) = %s, want %s", c.expr, c.from, got, c.want)
		}
	}
}

func TestNextNeverReturnsZeroAfterFiveYears(t *testing.T) {
	s, err := Parse("0 0 30 2 *") // February 30th does not exist
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(at(2026, 1, 1, 0, 0)); !got.IsZero() {
		t.Errorf("Next of an impossible date = %s, want zero time", got)
	}
}

func TestParseRejectsBadExpressions(t *testing.T) {
	bad := []string{
		"",
		"* * * *",     // too few fields
		"* * * * * *", // too many fields
		"61 * * * *",  // minute out of range
		"* 25 * * *",  // hour out of range
		"0 0 0 * *",   // day-of-month starts at 1
		"0 0 32 * *",
		"0 0 * 13 *",   // month out of range
		"0 0 * * 8",    // day-of-week out of range (7 is the max, = Sunday)
		"*/0 * * * *",  // zero step
		"*/-2 * * * *", // negative step
		"5-2 * * * *",  // inverted range
		"a b c d e",    // not numbers
		"@bogus",
		"@every 5m",    // not a cron expression; a different job kind
		"1,,2 * * * *", // empty list element
		"*-* * * * *",  // star in a range
		"jan-de * * * *",
	}
	for _, expr := range bad {
		if s, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) succeeded (%+v), want an error", expr, s)
		}
	}
}

func TestStandardRoundTrip(t *testing.T) {
	s, err := Parse("*/15 9-11 1,15 jan-mar mon-fri")
	if err != nil {
		t.Fatal(err)
	}
	want := "0,15,30,45 9,10,11 1,15 1,2,3 1,2,3,4,5"
	if got := s.Standard(); got != want {
		t.Errorf("Standard() = %q, want %q", got, want)
	}
	// A star stays a star.
	s, err = Parse("* * * * *")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Standard(); got != "* * * * *" {
		t.Errorf("Standard() = %q, want all stars", got)
	}
	// Standard() output parses to the same schedule, macros and names gone.
	s2, err := Parse(want)
	if err != nil {
		t.Fatalf("Standard() output does not re-parse: %v", err)
	}
	from := at(2026, 8, 22, 0, 0)
	orig, err := Parse("*/15 9-11 1,15 jan-mar mon-fri")
	if err != nil {
		t.Fatal(err)
	}
	if a, b := orig.Next(from), s2.Next(from); !a.Equal(b) {
		t.Errorf("re-parsed schedule disagrees: %s vs %s", a, b)
	}
}

// The location of the from time rules the result: a schedule is wall-clock.
func TestNextUsesTheGivenLocation(t *testing.T) {
	s, err := Parse("0 9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("UTC+8", 8*3600)
	from := time.Date(2026, 8, 22, 10, 0, 0, 0, loc)
	got := s.Next(from)
	if got.Location() != loc || got.Hour() != 9 || got.Day() != 23 {
		t.Errorf("Next in UTC+8 = %s, want 09:00 next day in UTC+8", got)
	}
}

func TestMacroExpansionRecordedInRaw(t *testing.T) {
	s, err := Parse("@daily")
	if err != nil {
		t.Fatal(err)
	}
	if s.Raw != "0 0 * * *" {
		t.Errorf("Raw = %q, want the expanded form", s.Raw)
	}
	if !strings.HasPrefix(s.Standard(), "0 0 ") {
		t.Errorf("Standard() = %q", s.Standard())
	}
}
