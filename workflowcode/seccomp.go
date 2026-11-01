package workflowcode

import "encoding/binary"

type sockFilter struct {
	Code   uint16
	JT, JF uint8
	K      uint32
}

func processFilterForArch(architecture string) ([]byte, error) {
	var arch uint32
	var clone, clone3 uint32
	var denied []uint32
	switch architecture {
	case "amd64":
		arch, clone, clone3 = 0xc000003e, 56, 435
		denied = []uint32{57, 58, 41, 42, 43, 49, 50, 53, 288, 101, 310, 311, 272, 308, 165, 166, 250, 321}
	case "arm64":
		arch, clone, clone3 = 0xc00000b7, 220, 435
		denied = []uint32{198, 199, 200, 201, 202, 203, 242, 117, 270, 271, 97, 268, 40, 39, 219, 280}
	default:
		return nil, fail("sandbox_unavailable", "当前CPU架构不支持代码隔离策略")
	}
	const load = 0x20
	const equal = 0x15
	const bits = 0x45
	const ret = 0x06
	const allow = 0x7fff0000
	const errno = 0x00050000
	const kill = 0x80000000
	program := []sockFilter{{load, 0, 0, 4}, {equal, 1, 0, arch}, {ret, 0, 0, kill}, {load, 0, 0, 0}}
	// Refuse x32 ABI aliases, which share AUDIT_ARCH_X86_64 but use different
	// syscall numbers. aarch64's valid numbers also stay below this boundary.
	program = append(program, sockFilter{0x35, 0, 1, 0x40000000}, sockFilter{ret, 0, 0, errno | 1})
	for _, number := range denied {
		program = append(program, sockFilter{equal, 0, 1, number}, sockFilter{ret, 0, 0, errno | 1})
	}
	program = append(program, sockFilter{equal, 0, 1, clone3}, sockFilter{ret, 0, 0, errno | 38})
	// Node/V8 needs threads. clone without CLONE_THREAD would create a new
	// process; rejecting it also blocks posix_spawn/subprocess/fork bombs.
	program = append(program, sockFilter{equal, 0, 4, clone}, sockFilter{load, 0, 0, 16}, sockFilter{bits, 1, 0, 0x10000}, sockFilter{ret, 0, 0, errno | 1}, sockFilter{ret, 0, 0, allow}, sockFilter{ret, 0, 0, allow})
	var data []byte
	for _, instruction := range program {
		data = binary.LittleEndian.AppendUint16(data, instruction.Code)
		data = append(data, instruction.JT, instruction.JF)
		data = binary.LittleEndian.AppendUint32(data, instruction.K)
	}
	return data, nil
}
