package observability

import "os"

// pidOf records the process id for diagnostics only. Liveness is decided by
// heartbeats, never by probing whether a PID still exists — PIDs get reused.
func pidOf() int { return os.Getpid() }
