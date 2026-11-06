package webservice

// This additional per-child filter stacks with Docker's existing seccomp
// policy; it never replaces that policy. The trusted Python supervisor installs
// it before exec, so Python, Node.js and their descendants accept incoming HTTP
// but cannot initiate networking, including loopback/Unix connections.
const pythonNetworkGuard = `import ctypes, errno, platform, sys
def _network_filter(machine):
    if machine in ('x86_64', 'amd64'):
        arch, connect_nr, sendto_nr, sendmsg_nr, sendmmsg_nr = 0xc000003e, 42, 44, 46, 307
    elif machine in ('aarch64', 'arm64'):
        arch, connect_nr, sendto_nr, sendmsg_nr, sendmmsg_nr = 0xc00000b7, 203, 206, 211, 269
    else:
        raise RuntimeError('unsupported runtime architecture')
    deny = 0x00050000 | errno.EPERM
    code = [(0x20, 0, 0, 4), (0x15, 1, 0, arch), (0x06, 0, 0, 0x80000000), (0x20, 0, 0, 0)]
    if arch == 0xc000003e:
        code += [(0x45, 0, 1, 0x40000000), (0x06, 0, 0, deny)]
    # Also deny io_uring's alternative async connect/send submission path.
    for nr in (connect_nr, sendmsg_nr, sendmmsg_nr, 425, 426, 427):
        code += [(0x15, 0, 1, nr), (0x06, 0, 0, deny)]
    # Destination pointer argument 4 starts at byte 48 in seccomp_data.
    # NULL is normal connected-socket send/sendall, including HTTP replies.
    code += [(0x15, 0, 5, sendto_nr), (0x20, 0, 0, 48), (0x15, 0, 2, 0),
             (0x20, 0, 0, 52), (0x15, 1, 0, 0), (0x06, 0, 0, deny), (0x06, 0, 0, 0x7fff0000)]
    return code
def _deny_outbound():
    if sys.platform != 'linux':
        raise RuntimeError('managed application networking requires Linux seccomp')
    code = _network_filter(platform.machine())
    class Filter(ctypes.Structure):
        _fields_ = [('code', ctypes.c_ushort), ('jt', ctypes.c_ubyte), ('jf', ctypes.c_ubyte), ('k', ctypes.c_uint32)]
    class Program(ctypes.Structure):
        _fields_ = [('length', ctypes.c_ushort), ('filters', ctypes.POINTER(Filter))]
    filters = (Filter * len(code))(*(Filter(*instruction) for instruction in code))
    program = Program(len(code), filters)
    libc = ctypes.CDLL(None, use_errno=True)
    libc.prctl.argtypes = [ctypes.c_int, ctypes.c_ulong, ctypes.c_ulong, ctypes.c_ulong, ctypes.c_ulong]
    libc.prctl.restype = ctypes.c_int
    if libc.prctl(38, 1, 0, 0, 0) != 0:
        raise OSError(ctypes.get_errno(), 'no-new-privileges unavailable')
    if libc.prctl(22, 2, ctypes.addressof(program), 0, 0) != 0:
        raise OSError(ctypes.get_errno(), 'managed network filter unavailable')
`
