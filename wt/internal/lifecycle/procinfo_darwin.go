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

// procWExit is P_WEXIT, the p_flag bit of a process that is working on exiting
// (sys/proc.h).
const procWExit = 0x00002000

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
	if errors.Is(err, unix.EIO) {
		// EIO alone does not say the process is going; kern.proc.pid has to.
		again, perr := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
		if perr != nil {
			return procInfo{}, false, fmt.Errorf("kern.proc.pid: %w", perr)
		}
		if !leavingTable(again, pid) {
			return procInfo{}, false, fmt.Errorf("kern.procargs2: %w", err)
		}
	}
	if info.argv, info.exiting, err = procArgv(raw, err); err != nil {
		return procInfo{}, false, err
	}
	return info, true, nil
}

// leavingTable reports whether a fresh kern.proc.pid answer for pid shows a
// process on its way out: gone, a zombie, or flagged P_WEXIT.
func leavingTable(kps []unix.KinfoProc, pid int) bool {
	if len(kps) != 1 || int(kps[0].Proc.P_pid) != pid {
		return true
	}
	p := kps[0].Proc
	return p.P_stat == procZombie || p.P_flag&procWExit != 0
}

// procArgv turns kern.procargs2's answer for a pid kern.proc.pid just listed
// into its argv. EINVAL and EIO there are not failures to read the table: they
// are a process with no arguments left to give, which the kernel is taking
// apart (procInfo.exiting). EINVAL is the usual answer; EIO was seen once on a
// CI runner in the same window (#481) and is treated alike, but only after
// realDescribeProc has confirmed from kern.proc.pid that the process is
// leaving: EIO on a live process stays an error. That
// lasts as long as freeing the process's memory does — measured at 7 to 13 ms
// per gigabyte, so a quarter of a second and more for an mtplx — and a caller
// waiting for the process to go must be able to keep waiting through it
// instead of being told the table is unreadable. Any other errno is an error.
func procArgv(raw []byte, err error) (argv []string, exiting bool, _ error) {
	switch {
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.EIO):
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
