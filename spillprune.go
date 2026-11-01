package main

import (
	"fmt"
	"io"
	"time"

	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/tools"
)

// spillPruneCommand implements -spill-prune.
//
// The CLI form of spill cleanup is a command, not a background sweep, for the
// same reason -checkpoints-prune is (see checkpointCommand): pogo has no
// daemon, so the honest options are "when a person asks" and "never". The web
// server is the one long-lived process and owns the timed sweep
// (web/spillsweep.go); this command is the same retention applied by hand,
// reading the same prefs.json so the two never disagree about the policy.
//
// Two failure rules mirror the sweeper's: a prefs file that cannot be read
// is an error, never a fallback to the default (deleting on a guess is the
// unrecoverable direction); and retention 0 means "disabled", printed as an
// instruction rather than silently pruning with a zero window — which would
// delete everything.
func spillPruneCommand(out io.Writer, daysOverride int) error {
	if daysOverride < 0 {
		return fmt.Errorf("-spill-prune-days must be positive (got %d)", daysOverride)
	}
	p, err := prefs.Load(prefs.Path())
	if err != nil {
		return err
	}
	days := p.SpillRetentionDays
	if daysOverride > 0 {
		days = daysOverride
	}
	if days == 0 {
		fmt.Fprintln(out, "spill auto-cleanup is disabled (spill_retention_days=0); use -spill-prune-days N to force a run."+
			"\n自动清理已禁用（spill_retention_days=0），可用 -spill-prune-days N 强制清理一次")
		return nil
	}

	dir := tools.DefaultSpillDir()
	res, err := tools.PruneSpill(dir, time.Duration(days)*24*time.Hour, time.Now())
	if err != nil {
		return err
	}
	// Usage after the pass, so "kept" is what is actually on disk rather than
	// arithmetic on a scan that raced live writers.
	files, bytes, err := tools.SpillDiskUsage(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %d files (%.1f MB), kept %d files (%.1f MB), retention %d days\n",
		res.Removed, float64(res.Bytes)/(1<<20), files, float64(bytes)/(1<<20), days)
	return nil
}
