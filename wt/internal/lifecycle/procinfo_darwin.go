package lifecycle

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

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
	for attempt := 0; ; attempt++ {
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
		if err == nil {
			if info.argv, err = parseProcArgs(raw); err != nil {
				return procInfo{}, false, err
			}
			return info, true, nil
		}
		// A process on its way out is still in the table, with no arguments
		// to give (EINVAL), for a moment before it becomes a zombie or
		// disappears. Read it again until it has, for up to 200ms.
		if attempt == procArgsRetries {
			return procInfo{}, false, fmt.Errorf("kern.procargs2: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// procArgsRetries bounds realDescribeProc's wait for an exiting process.
const procArgsRetries = 20

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
