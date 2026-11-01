package workflowcode

import (
	"encoding/binary"
	"testing"
)

// Evaluate the generated kernel program against representative syscall data.
// This runs on macOS too; actual Linux namespace enforcement is additionally
// exercised by the execution tests whenever bubblewrap is available.
func evaluatePolicy(t *testing.T, data []byte, arch, number, flags uint32) uint32 {
	t.Helper()
	var accumulator uint32
	for pc := 0; pc < len(data)/8; pc++ {
		ins := data[pc*8 : pc*8+8]
		code := binary.LittleEndian.Uint16(ins)
		value := binary.LittleEndian.Uint32(ins[4:])
		switch code {
		case 0x20:
			switch value {
			case 0:
				accumulator = number
			case 4:
				accumulator = arch
			case 16:
				accumulator = flags
			default:
				t.Fatalf("unknown seccomp load %d", value)
			}
		case 0x15:
			if accumulator == value {
				pc += int(ins[2])
			} else {
				pc += int(ins[3])
			}
		case 0x35:
			if accumulator >= value {
				pc += int(ins[2])
			} else {
				pc += int(ins[3])
			}
		case 0x45:
			if accumulator&value != 0 {
				pc += int(ins[2])
			} else {
				pc += int(ins[3])
			}
		case 0x06:
			return value
		default:
			t.Fatalf("unknown seccomp instruction %x", code)
		}
	}
	t.Fatal("seccomp policy did not return")
	return 0
}

func TestSeccompPolicyDeniesProcessesAndSockets(t *testing.T) {
	for _, tc := range []struct {
		architecture                   string
		arch, clone, socket, processVM uint32
		forks                          []uint32
	}{
		{"amd64", 0xc000003e, 56, 41, 310, []uint32{57, 58}},
		{"arm64", 0xc00000b7, 220, 198, 270, nil},
	} {
		t.Run(tc.architecture, func(t *testing.T) {
			policy, err := processFilterForArch(tc.architecture)
			if err != nil {
				t.Fatal(err)
			}
			for _, number := range append([]uint32{tc.clone, tc.socket, tc.processVM}, tc.forks...) {
				if got := evaluatePolicy(t, policy, tc.arch, number, 0); got != 0x00050001 {
					t.Errorf("syscall %d escaped: %x", number, got)
				}
			}
			if got := evaluatePolicy(t, policy, tc.arch, tc.clone, 0x10000|0x100|0x800); got != 0x7fff0000 {
				t.Errorf("V8 thread denied: %x", got)
			}
			if got := evaluatePolicy(t, policy, tc.arch, 435, 0); got != 0x00050026 {
				t.Errorf("clone3 must return ENOSYS: %x", got)
			}
			if got := evaluatePolicy(t, policy, tc.arch, 0, 0); got != 0x7fff0000 {
				t.Errorf("ordinary read denied: %x", got)
			}
			if got := evaluatePolicy(t, policy, 0, 0, 0); got != 0x80000000 {
				t.Errorf("alternate architecture escaped: %x", got)
			}
			if got := evaluatePolicy(t, policy, tc.arch, 0x40000001, 0); got != 0x00050001 {
				t.Errorf("alternate ABI escaped: %x", got)
			}
		})
	}
	if _, err := processFilterForArch("unknown"); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
}
