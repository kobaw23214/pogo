package session

import (
	"errors"
	"os"
	"testing"
)

func TestDelegatedStatsReceiptSurvivesReopenAndRewind(t *testing.T) {
	s, err := Create(t.TempDir(), "/workspace", "model")
	if err != nil {
		t.Fatal(err)
	}
	u := UsageStats{Input: 101, Output: 17}
	if fresh, err := s.AppendDelegatedStatsOnce("run-a", u); err != nil || !fresh {
		t.Fatalf("first: %v %v", fresh, err)
	}
	// This is the crash-after-fsync-before-notification window: the next host
	// sees the receipt and its charge together and cannot bill it again.
	s, err = Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fresh, err := s.AppendDelegatedStatsOnce("run-a", u); err != nil || fresh {
		t.Fatalf("duplicate after restart: %v %v", fresh, err)
	}
	if err := s.Fork(""); err != nil {
		t.Fatal(err)
	}
	if fresh, err := s.AppendDelegatedStatsOnce("run-a", u); err != nil || fresh {
		t.Fatalf("duplicate after rewind: %v %v", fresh, err)
	}
	if fresh, err := s.AppendDelegatedStatsOnce("run-b", UsageStats{Input: 33, Output: 4}); err != nil || !fresh {
		t.Fatalf("second: %v %v", fresh, err)
	}
	uTotal, d := s.UsageTotals()
	if uTotal.Input != 134 || d.Input != 134 || uTotal.Output != 21 {
		t.Fatalf("totals: %+v %+v", uTotal, d)
	}
}

func TestDelegatedStatsFailedAppendCanRetryWithoutASeparateReceipt(t *testing.T) {
	s, err := Create(t.TempDir(), "/workspace", "model")
	if err != nil {
		t.Fatal(err)
	}
	s.writeFile = func(f *os.File, b []byte) error {
		_, err := f.Write(b)
		if err != nil {
			return err
		}
		return errors.New("simulated failure after bytes were written")
	}
	if fresh, err := s.AppendDelegatedStatsOnce("run-a", UsageStats{Input: 10}); err == nil || fresh {
		t.Fatal("failed write accepted")
	}
	s.writeFile = nil
	s, err = Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fresh, err := s.AppendDelegatedStatsOnce("run-a", UsageStats{Input: 10}); err != nil || !fresh {
		t.Fatalf("retry: %v %v", fresh, err)
	}
	u, _ := s.UsageTotals()
	if u.Input != 10 {
		t.Fatalf("retry billed twice: %+v", u)
	}
}

func TestDelegatedStatsZeroUsageAndNegativeDelta(t *testing.T) {
	s, err := Create(t.TempDir(), "/workspace", "model")
	if err != nil {
		t.Fatal(err)
	}
	if fresh, err := s.AppendDelegatedStatsOnce("zero", UsageStats{}); err != nil || !fresh {
		t.Fatal(err)
	}
	if fresh, err := s.AppendDelegatedStatsOnce("zero", UsageStats{}); err != nil || fresh {
		t.Fatal("zero usage completion was not idempotent")
	}
	if _, err := s.AppendDelegatedStatsOnce("negative", UsageStats{Input: -1}); err == nil {
		t.Fatal("negative delta accepted")
	}
}
