package lifecycle

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// procZombie is SZOMB, the p_stat of a process that has exited and not been
// reaped (sys/proc.h).
const procZombie = 5

// realDescribeProc reads pid from the kernel's process table: kern.proc.pid
// for the owner, the group, the state and the start time, and — for a live
// process of the current user — kern.procargs2 for the exact argv. No `ps`:
// its command column joins the arguments with spaces, and mtplx's own paths
// contain one.
func realDescribeProc(pid int) (procInfo, bool, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return procInfo{}, false, fmt.Errorf("kern.proc.pid: %w", err)
	}
	if len(kps) != 1 || int(kps[0].Proc.P_pid) != pid {
		return procInfo{}, false, nil
	}
	kp := kps[0]
	info := procInfo{
		uid:    int(kp.Eproc.Ucred.Uid),
		pgid:   int(kp.Eproc.Pgid),
		zombie: kp.Proc.P_stat == procZombie,
		start:  fmt.Sprintf("%d.%06d", kp.Proc.P_starttime.Sec, kp.Proc.P_starttime.Usec),
	}
	if info.zombie || info.uid != unix.Getuid() {
		return info, true, nil // the kernel gives argv for the caller's own live processes only
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if info.argv, info.exiting, err = procArgv(raw, err); err != nil {
		return procInfo{}, false, err
	}
	return info, true, nil
}

// procArgv turns kern.procargs2's answer for a pid kern.proc.pid just listed
// into its argv. EINVAL there is not a failure to read the table: it is a
// process with no arguments left to give, which the kernel is taking apart
// (procInfo.exiting). That lasts as long as freeing its memory does — measured
// at 7 to 13 ms per gigabyte, so a quarter of a second and more for an mtplx —
// and a caller waiting for the process to go must be able to keep waiting
// through it instead of being told the table is unreadable.
func procArgv(raw []byte, err error) (argv []string, exiting bool, _ error) {
	switch {
	case errors.Is(err, unix.EINVAL):
		return nil, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("kern.procargs2: %w", err)
	}
	argv, err = parseProcArgs(raw)
	return argv, false, err
}

// parseProcArgs reads argv out of a kern.procargs2 buffer: a native-endian
// int32 argc, the executable's path, NUL padding, then argc NUL-terminated
// arguments (the environment follows and is not read).
func parseProcArgs(raw []byte) ([]string, error) {
	if len(raw) < 4 {
		return nil, errors.New("kern.procargs2: short buffer")
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return nil, errors.New("kern.procargs2: no executable path")
	}
	rest = bytes.TrimLeft(rest[end:], "\x00")
	argv := make([]string, 0, argc)
	for len(argv) < argc {
		end = bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil, errors.New("kern.procargs2: truncated arguments")
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return argv, nil
}
