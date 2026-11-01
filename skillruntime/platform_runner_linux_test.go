//go:build linux

package skillruntime

import "testing"

func TestPlatformRunnerClockTicks(t *testing.T) {
	for _, frequency := range []uintptr{100, 250, 1000} {
		got, err := clockTicksFromAuxv([][2]uintptr{{6, 4096}, {17, frequency}, {0, 0}})
		if err != nil || got != uint64(frequency) {
			t.Fatalf("kernel frequency %d lost: %d %v", frequency, got, err)
		}
	}
	for _, malformed := range [][][2]uintptr{nil, {{6, 4096}}, {{17, 0}}, {{17, 1000001}}} {
		if _, err := clockTicksFromAuxv(malformed); err == nil {
			t.Fatalf("invalid kernel frequency accepted: %v", malformed)
		}
	}
	if ticks, err := localClockTicks(); err != nil || ticks == 0 {
		t.Fatalf("current Linux kernel cannot support CPU monitoring: %d %v", ticks, err)
	}
}
