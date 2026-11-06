package webservice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func pythonForNetworkTest(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	return p
}

// Interpret the actual program emitted by the Python child bootstrap. This
// validates both audit architectures and every jump without installing a filter
// on the host test process, including when tests run on macOS.
func TestManagedPythonNetworkFilterSemantics(t *testing.T) {
	for _, tc := range []struct {
		machine                                  string
		arch, connect, sendto, sendmsg, sendmmsg uint32
	}{{"x86_64", 0xc000003e, 42, 44, 46, 307}, {"aarch64", 0xc00000b7, 203, 206, 211, 269}} {
		t.Run(tc.machine, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, pythonForNetworkTest(t), "-I", "-c", pythonNetworkGuard+"\nimport json\nprint(json.dumps(_network_filter('"+tc.machine+"')))\n").CombinedOutput()
			if err != nil {
				t.Fatalf("generate filter: %s %v", out, err)
			}
			var program [][4]uint32
			if err := json.Unmarshal(out, &program); err != nil {
				t.Fatal(err)
			}
			for _, nr := range []uint32{tc.connect, tc.sendmsg, tc.sendmmsg, 425, 426, 427} {
				if got := runNetworkFilter(t, program, tc.arch, nr, 0); got != 0x50001 {
					t.Fatalf("syscall %d was not denied: %#x", nr, got)
				}
			}
			for _, destination := range []uint64{1, 0x100000000, 0x100000001} {
				if got := runNetworkFilter(t, program, tc.arch, tc.sendto, destination); got != 0x50001 {
					t.Fatalf("destination %#x allowed: %#x", destination, got)
				}
			}
			if got := runNetworkFilter(t, program, tc.arch, tc.sendto, 0); got != 0x7fff0000 {
				t.Fatalf("HTTP sendall denied: %#x", got)
			}
			if got := runNetworkFilter(t, program, tc.arch, 1, 0); got != 0x7fff0000 {
				t.Fatalf("ordinary IO denied: %#x", got)
			}
			if got := runNetworkFilter(t, program, 0x40000003, 1, 0); got != 0x80000000 {
				t.Fatalf("foreign audit arch allowed: %#x", got)
			}
			if tc.machine == "x86_64" {
				if got := runNetworkFilter(t, program, tc.arch, 0x40000000|tc.connect, 0); got != 0x50001 {
					t.Fatalf("x32 ABI bypass: %#x", got)
				}
			}
		})
	}
}

func runNetworkFilter(t *testing.T, program [][4]uint32, arch, nr uint32, dest uint64) uint32 {
	t.Helper()
	data := make([]byte, 64)
	binary.LittleEndian.PutUint32(data, nr)
	binary.LittleEndian.PutUint32(data[4:], arch)
	binary.LittleEndian.PutUint64(data[48:], dest)
	var a uint32
	for pc, steps := 0, 0; pc >= 0 && pc < len(program) && steps < 100; steps++ {
		instruction := program[pc]
		pc++
		switch instruction[0] {
		case 0x20:
			offset := int(instruction[3])
			if offset < 0 || offset+4 > len(data) {
				t.Fatal("filter load out of bounds")
			}
			a = binary.LittleEndian.Uint32(data[offset:])
		case 0x15:
			if a == instruction[3] {
				pc += int(instruction[1])
			} else {
				pc += int(instruction[2])
			}
		case 0x45:
			if a&instruction[3] != 0 {
				pc += int(instruction[1])
			} else {
				pc += int(instruction[2])
			}
		case 0x06:
			return instruction[3]
		default:
			t.Fatalf("unknown BPF instruction %#x", instruction[0])
		}
	}
	t.Fatal("filter failed to terminate")
	return 0
}

func TestManagedPythonNetworkFilterLinuxEnforcement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual seccomp enforcement requires Linux; QA container runs this path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	script := pythonNetworkGuard + `
import socket, json
_deny_outbound()
checks = {}
for label, action in [('tcp', lambda: socket.socket().connect(('127.0.0.1', 9))),
                      ('udp', lambda: socket.socket(socket.AF_INET, socket.SOCK_DGRAM).sendto(b'x', ('127.0.0.1', 9)))]:
    try:
        action()
        checks[label] = False
    except OSError as error:
        checks[label] = error.errno == errno.EPERM
server = socket.socket()
server.bind(('127.0.0.1', 0))
server.listen(1)
checks['port'] = server.getsockname()[1]
print(json.dumps(checks), flush=True)
client, _ = server.accept()
assert client.recv(4) == b'PING'
client.sendall(b'PONG')
client.close()
server.close()
`
	command := exec.CommandContext(ctx, pythonForNetworkTest(t), "-I", "-c", script)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatalf("network guard startup failed: %s %v", stderr.String(), err)
	}
	var result struct {
		TCP, UDP bool
		Port     int
	}
	if err := json.Unmarshal(line, &result); err != nil {
		t.Fatal(err)
	}
	if !result.TCP || !result.UDP {
		t.Fatalf("active networking was not denied: %+v", result)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(result.Port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("PING"))
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil || string(response) != "PONG" {
		t.Fatalf("inbound HTTP-style reply broken: %q %v %s", response, err, stderr.String())
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("guarded process failed: %s %v", stderr.String(), err)
	}
}
