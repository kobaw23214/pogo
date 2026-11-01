//go:build unix

package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// AdoptSubagentEditLease keeps the inherited lease until this Pogo process exits,
// but prevents shell commands and other tool processes from retaining it. The
// raw descriptor intentionally has no os.File finalizer. Unsetting the marker
// makes startup checks idempotent and avoids forwarding stale descriptor numbers.
func AdoptSubagentEditLease() error {
	for _, name := range []string{EnvSubagentEditLeaseFD, envSubagentContextLeaseFD} {
		raw, present := os.LookupEnv(name)
		if !present {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			return err
		}
		fd, err := strconv.Atoi(raw)
		if err != nil || fd < 3 {
			return errors.New("invalid delegation lease descriptor")
		}
		if err := adoptEditLeaseFD(fd); err != nil {
			return err
		}
	}
	return nil
}

func adoptEditLeaseFD(fd int) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&0077 != 0 {
		return errors.New("invalid inherited writable delegation lease")
	}
	syscall.CloseOnExec(fd)
	return nil
}

// One OS user has one writer lease on this host, independent of session, cwd,
// HOME and TMPDIR. The file is never unlinked: replacing it could give concurrent
// callers different inodes and therefore different locks. This serializes Pogo
// writable delegations, not editors or other top-level conversations.
func acquireEditLease(ctx context.Context) (*os.File, error) {
	path := filepath.Join("/tmp", fmt.Sprintf("pogo-subagent-edit-%d.lock", os.Geteuid()))
	return acquireEditLeaseAt(ctx, path)
}

func acquireEditLeaseAt(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open writable delegation lease: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&0077 != 0 {
		f.Close()
		return nil, errors.New("writable delegation lease must be a private regular file owned by this OS user")
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			f.Close()
			return nil, fmt.Errorf("acquire writable delegation lease: %w", err)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
