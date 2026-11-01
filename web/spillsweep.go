package web

// The spill directory's automatic sweeper.
//
// The CLI philosophy is that cleanup is a command, not a background sweep,
// because the terminal process has no business deleting things inside an
// unrelated run (see checkpointCommand). The web server is the one long-lived
// process pogo has — the same reason reap() may evict idle sessions — so it
// is also the one place a timed sweep is honest: it runs once at startup and
// then on an interval, and what it deletes is governed by a preference the
// user can see and set in the same UI.
//
// The safety rule is asymmetric on purpose: a preferences file that cannot be
// read pauses the sweep with one log line. Falling back to the default
// retention would mean a typo'd hand-edit silently restores 7 days and the
// next pass deletes what the operator asked to keep — deleting too much is
// the unrecoverable direction.

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/tools"
)

// DefaultSpillSweepInterval is how often the web server re-runs the sweep
// after the startup pass. Rare enough that the directory walk is noise,
// often enough that a runaway agent's output cannot pile up for days.
const DefaultSpillSweepInterval = 6 * time.Hour

// errSpillPruneDisabled distinguishes "retention is 0" from a real failure:
// the sweeper treats it as a configuration and stays quiet, while the
// manual-prune endpoint turns it into a 400 the user can read.
var errSpillPruneDisabled = errors.New("automatic spill cleanup is disabled (spill_retention_days=0)")

// SpillSweep is the outcome of one completed pass, kept for GET /api/prefs so
// the settings dialog can answer "did the cleaner ever run, and did it do
// anything" without anyone reading logs.
type SpillSweep struct {
	Time    time.Time `json:"time"`
	Removed int       `json:"removed"`
	Bytes   int64     `json:"bytes"`
}

// sweepSpill runs once at startup and then per cfg.SpillSweepInterval, the
// same wg + stop-channel shape as reap().
func (m *Manager) sweepSpill() {
	defer m.wg.Done()
	m.sweepSpillOnce(time.Now())
	t := time.NewTicker(m.cfg.SpillSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-t.C:
			m.sweepSpillOnce(now)
		}
	}
}

// sweepSpillOnce is the goroutine's body with the clock taken out, which is
// what makes "the sweeper is really wired up" testable: a test ages a file
// into a temp POGO_SPILL_DIR and calls this, rather than waiting out a
// ticker. A cleanup function that exists but is never called is how a spill
// directory grows to tens of gigabytes — the test that calls this directly
// is the guard against that exact regression.
func (m *Manager) sweepSpillOnce(now time.Time) {
	// A release keeps the original files frozen until preservation checks
	// finish. In particular, a freshly restarted process must not prune an
	// expired result between the cold backup and those checks. The existing
	// marker reader also treats unreadable or malformed state as maintenance.
	if active, _ := m.releaseMaintenance(); active {
		return
	}
	res, err := m.pruneSpill(now)
	switch {
	case errors.Is(err, errSpillPruneDisabled):
		// Disabled is a setting, not a failure; logging it every interval
		// would teach people to stop reading the log.
	case err != nil:
		fmt.Fprintf(os.Stderr, "pogo: spill sweep paused: %v\n", err)
	case res.Removed > 0:
		fmt.Fprintf(os.Stderr, "pogo: spill sweep: removed %d file(s), %d bytes\n", res.Removed, res.Bytes)
	}
}

// pruneSpill runs one retention pass over the spill directory and records
// the outcome. Shared by the background sweeper (errors become a log line)
// and POST /api/prefs/prune-spill (errors become an HTTP status), so the two
// can never disagree about what "a sweep" is.
func (m *Manager) pruneSpill(now time.Time) (tools.PruneResult, error) {
	p, err := prefs.Load(prefs.Path())
	if err != nil {
		return tools.PruneResult{}, err
	}
	if p.SpillRetentionDays == 0 {
		return tools.PruneResult{}, errSpillPruneDisabled
	}
	res, err := tools.PruneSpill(tools.DefaultSpillDir(),
		time.Duration(p.SpillRetentionDays)*24*time.Hour, now)
	if err != nil {
		return res, err
	}
	m.sweepMu.Lock()
	m.lastSweep = &SpillSweep{Time: now, Removed: res.Removed, Bytes: res.Bytes}
	m.sweepMu.Unlock()
	return res, nil
}

// LastSpillSweep reports the most recent completed pass, nil before the
// first one. A copy, so a handler reading it races neither the next sweep
// nor its own JSON encoder. Guarded by its own mutex rather than m.mu: the
// session lock is held across run orchestration, and a cleaner has no
// business queueing behind it.
func (m *Manager) LastSpillSweep() *SpillSweep {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	if m.lastSweep == nil {
		return nil
	}
	cp := *m.lastSweep
	return &cp
}
