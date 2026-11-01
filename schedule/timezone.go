package schedule

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // minimal Docker/ARM deployments must not depend on OS zone files
)

// ServerTimezone describes the legacy empty-zone behavior without silently
// substituting a caller's browser zone. Unknown local-zone identities remain
// explicit server-local values rather than fabricated IANA names.
func ServerTimezone() (zone, label string) {
	candidates := []string{time.Local.String(), strings.TrimPrefix(os.Getenv("TZ"), ":")}
	if link, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(link, "/zoneinfo/"); i >= 0 {
			candidates = append(candidates, link[i+len("/zoneinfo/"):])
		}
	}
	for _, name := range candidates {
		if name == "" || name == "Local" {
			continue
		}
		loc, err := Location(name)
		if err != nil {
			continue
		}
		agrees := true
		for year := time.Now().Year(); year <= time.Now().Year()+5; year++ {
			for _, month := range []time.Month{1, 4, 7, 10} {
				t := time.Date(year, month, 15, 12, 0, 0, 0, time.UTC)
				_, a := t.In(loc).Zone()
				_, b := t.In(time.Local).Zone()
				if a != b {
					agrees = false
				}
			}
		}
		if agrees {
			return name, name
		}
	}
	name, offset := time.Now().In(time.Local).Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return "", fmt.Sprintf("服务器本地时区 (%s UTC%s%02d:%02d)", name, sign, offset/3600, offset%3600/60)
}
func EffectiveTimezone(zone string) (string, string) {
	if zone != "" {
		return zone, zone
	}
	return ServerTimezone()
}
