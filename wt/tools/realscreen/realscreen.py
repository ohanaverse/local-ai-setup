#!/usr/bin/env python3
"""Capture what a TUI really draws at a fixed terminal size.

Runs a command under a pty of --cols x --rows, answers the terminal queries a
TUI waits on (OSC 10/11/12 colours and the cursor position report; without the
answers a bubbletea program draws nothing), types --keys, rebuilds the screen
from the bytes with a small VT100 interpreter and prints it as text.

Stdlib only; run it as `python3 -I realscreen.py ...`.
Self-check without a TUI: `python3 -I realscreen.py --selftest`.
"""
import argparse
import errno
import fcntl
import os
import pty
import re
import select
import shutil
import signal
import struct
import sys
import tempfile
import termios
import time

CSI_RE = re.compile(rb"\x1b\[([0-9;?<>=!]*)([ -/]*)([@-~])")
OSC_RE = re.compile(rb"\x1b\](\d+);([^\x07\x1b]*)(\x07|\x1b\\)")
QUERY_RE = re.compile(rb"\x1b\](1[012]);\?(\x07|\x1b\\)|\x1b\[6n")


class Screen:
    """A rows x cols text grid driven by VT100 bytes (SGR and styling ignored)."""

    def __init__(self, rows, cols):
        self.rows, self.cols = rows, cols
        self.main = self._blank()
        self.alt = self._blank()
        self.on_alt = False
        self.row = self.col = 0
        self.saved = (0, 0)
        self.pending = b""
        self.wrap_pending = False

    def _blank(self):
        return [[" "] * self.cols for _ in range(self.rows)]

    @property
    def grid(self):
        return self.alt if self.on_alt else self.main

    def feed(self, data):
        data = self.pending + data
        self.pending = b""
        i, n = 0, len(data)
        while i < n:
            b = data[i]
            if b == 0x1B:
                if i + 1 >= n:
                    self.pending = data[i:]
                    return
                nxt = data[i + 1]
                if nxt == 0x5B:  # CSI
                    m = CSI_RE.match(data, i)
                    if not m:
                        if n - i < 64 and not re.search(rb"[@-~]", data[i + 2:]):
                            self.pending = data[i:]
                            return
                        i += 2
                        continue
                    self.csi(m.group(1).decode(), m.group(3).decode())
                    i = m.end()
                elif nxt == 0x5D:  # OSC: skip to BEL or ST
                    end = n
                    for j in range(i + 2, n):
                        if data[j] == 0x07:
                            end = j + 1
                            break
                        if data[j] == 0x1B and j + 1 < n and data[j + 1] == 0x5C:
                            end = j + 2
                            break
                    else:
                        self.pending = data[i:]
                        return
                    i = end
                elif nxt in b"()":  # charset designation, one more byte
                    i += 3
                elif nxt == 0x37:  # ESC 7
                    self.saved = (self.row, self.col)
                    i += 2
                elif nxt == 0x38:  # ESC 8
                    self.row, self.col = self.saved
                    i += 2
                elif nxt == 0x4D:  # reverse index
                    self.row = max(0, self.row - 1)
                    i += 2
                else:
                    i += 2
            elif b == 0x0A:
                self.linefeed()
                i += 1
            elif b == 0x0D:
                self.col = 0
                self.wrap_pending = False
                i += 1
            elif b == 0x08:
                self.col = max(0, self.col - 1)
                i += 1
            elif b == 0x09:
                self.col = min(self.cols - 1, (self.col // 8 + 1) * 8)
                i += 1
            elif b < 0x20 or b == 0x7F:
                i += 1
            else:
                # one UTF-8 character
                ln = 1 if b < 0x80 else 2 if b < 0xE0 else 3 if b < 0xF0 else 4
                if i + ln > n:
                    self.pending = data[i:]
                    return
                self.put(data[i:i + ln].decode("utf-8", "replace"))
                i += ln

    def put(self, ch):
        if self.wrap_pending:
            self.col = 0
            self.linefeed()
            self.wrap_pending = False
        self.grid[self.row][self.col] = ch
        if self.col == self.cols - 1:
            self.wrap_pending = True
        else:
            self.col += 1

    def linefeed(self):
        self.wrap_pending = False
        if self.row == self.rows - 1:
            g = self.grid
            g.pop(0)
            g.append([" "] * self.cols)
        else:
            self.row += 1

    def csi(self, params, final):
        priv = params[:1] in ("?", "<", ">", "=")
        nums = [int(p) if p.isdigit() else 0 for p in params.lstrip("?<>=").split(";")] if params else []
        arg = lambda k, d: nums[k] if len(nums) > k and nums[k] else d
        self.wrap_pending = False
        if final in "Hf" and not priv:
            self.row = min(self.rows - 1, arg(0, 1) - 1)
            self.col = min(self.cols - 1, arg(1, 1) - 1)
        elif final == "A":
            self.row = max(0, self.row - arg(0, 1))
        elif final == "B":
            self.row = min(self.rows - 1, self.row + arg(0, 1))
        elif final == "C":
            self.col = min(self.cols - 1, self.col + arg(0, 1))
        elif final == "D":
            self.col = max(0, self.col - arg(0, 1))
        elif final == "G":
            self.col = min(self.cols - 1, arg(0, 1) - 1)
        elif final == "d":
            self.row = min(self.rows - 1, arg(0, 1) - 1)
        elif final == "J":
            mode = nums[0] if nums else 0
            g = self.grid
            if mode == 0:
                g[self.row][self.col:] = [" "] * (self.cols - self.col)
                for r in range(self.row + 1, self.rows):
                    g[r] = [" "] * self.cols
            elif mode == 1:
                for r in range(self.row):
                    g[r] = [" "] * self.cols
                g[self.row][:self.col + 1] = [" "] * (self.col + 1)
            else:
                for r in range(self.rows):
                    g[r] = [" "] * self.cols
        elif final == "K":
            mode = nums[0] if nums else 0
            line = self.grid[self.row]
            if mode == 0:
                line[self.col:] = [" "] * (self.cols - self.col)
            elif mode == 1:
                line[:self.col + 1] = [" "] * (self.col + 1)
            else:
                line[:] = [" "] * self.cols
        elif final in "hl" and priv:
            if any(v in (47, 1047, 1049) for v in nums):
                self.on_alt = final == "h"
                if self.on_alt:
                    self.alt = self._blank()
                    self.row = self.col = 0
        elif final == "s":
            self.saved = (self.row, self.col)
        elif final == "u":
            self.row, self.col = self.saved
        # m (SGR), r, q and the rest are ignored.

    def text(self):
        lines = ["".join(r).rstrip() for r in self.grid]
        while lines and not lines[-1]:
            lines.pop()
        return "\n".join(lines)


def answers(chunk, rows_cols, screen):
    """Replies to the terminal queries found in chunk."""
    out = b""
    for m in QUERY_RE.finditer(chunk):
        if m.group(0) == b"\x1b[6n":
            out += b"\x1b[%d;%dR" % (screen.row + 1, screen.col + 1)
        else:
            n = m.group(1)
            colour = {b"10": b"ffff/ffff/ffff", b"11": b"0000/0000/0000", b"12": b"ffff/ffff/ffff"}[n]
            out += b"\x1b]" + n + b";rgb:" + colour + b"\x1b\\"
    return out


def unescape_keys(s):
    return s.encode().decode("unicode_escape").encode("latin-1")


def kill_tree(pid, pgid):
    for sig in (signal.SIGTERM, signal.SIGKILL):
        try:
            os.killpg(pgid, sig)
        except (ProcessLookupError, PermissionError):
            pass
        time.sleep(0.1)
    try:
        os.waitpid(pid, 0)
    except ChildProcessError:
        pass


def run(args):
    xdg_tmp = None
    env = dict(os.environ)
    if args.xdg:
        env["XDG_CONFIG_HOME"] = args.xdg
    else:
        xdg_tmp = tempfile.mkdtemp(prefix="realscreen-xdg-")
        env["XDG_CONFIG_HOME"] = xdg_tmp
    env["TERM"] = "xterm-256color"
    env["LINES"], env["COLUMNS"] = str(args.rows), str(args.cols)
    pid, fd = pty.fork()
    if pid == 0:
        try:
            # pty.fork made the child a session leader: its pid is its pgid.
            fcntl.ioctl(0, termios.TIOCSWINSZ, struct.pack("HHHH", args.rows, args.cols, 0, 0))
            if args.cwd:
                os.chdir(args.cwd)
            os.execvpe(args.cmd[0], args.cmd, env)
        except Exception as e:  # noqa: BLE001
            sys.stderr.write("realscreen: exec failed: %s\n" % e)
        os._exit(127)
    pgid = pid
    screen = Screen(args.rows, args.cols)
    keys = unescape_keys(args.keys) if args.keys else b""
    keys_sent = not keys
    last = time.time()
    start = time.time()
    exited = False
    try:
        while time.time() - start < args.max_seconds:
            r, _, _ = select.select([fd], [], [], 0.1)
            if r:
                try:
                    chunk = os.read(fd, 65536)
                except OSError as e:
                    if e.errno == errno.EIO:
                        exited = True
                        break
                    raise
                if not chunk:
                    exited = True
                    break
                last = time.time()
                screen.feed(chunk)
                reply = answers(chunk, (args.rows, args.cols), screen)
                if reply:
                    os.write(fd, reply)
                continue
            idle = time.time() - last
            if not keys_sent and idle >= args.idle:
                os.write(fd, keys)
                keys_sent = True
                last = time.time()
            elif keys_sent and idle >= args.idle:
                break
        # The screen worth reading is the settled one, taken before ctrl+c
        # tears the alt screen down.
        settled = screen.text()
        if not exited:
            try:
                os.write(fd, b"\x03")
            except OSError:
                pass
            end = time.time() + 1.0
            while time.time() < end:
                r, _, _ = select.select([fd], [], [], 0.1)
                if r:
                    try:
                        chunk = os.read(fd, 65536)
                    except OSError:
                        break
                    if not chunk:
                        break
                    screen.feed(chunk)
    finally:
        kill_tree(pid, pgid)
        os.close(fd)
        if xdg_tmp:
            shutil.rmtree(xdg_tmp, ignore_errors=True)
    return settled


def selftest():
    s = Screen(5, 20)
    s.feed(b"\x1b[?1049h\x1b[2J\x1b[H\x1b[1;32mhello\x1b[0m\r\nworld")
    s.feed(b"\x1b[3;5Hmid\x1b[K\x1b[5;1Hx\x1b[1;1H\x1b[2K\x1b[1;3Hab")
    s.feed("│ café".encode()[:3])  # split UTF-8 character
    s.feed("│ café".encode()[3:])
    want = "  ab│ café\nworld\n    mid\n\nx"
    got = s.text()
    # Row 1 was erased then rewritten at col 3, so the leading text is blanks.
    assert got.splitlines()[0].rstrip() == "  ab│ café", repr(got)
    assert got.splitlines()[1] == "world", repr(got)
    assert got.splitlines()[2] == "    mid", repr(got)
    assert got.splitlines()[4] == "x", repr(got)
    # Leaving the alt screen restores the main grid (empty here).
    s.feed(b"\x1b[?1049l")
    assert s.text() == "", repr(s.text())
    # Wrap at the last column.
    w = Screen(2, 4)
    w.feed(b"abcdef")
    assert w.text() == "abcd\nef", repr(w.text())
    # Scroll when a linefeed passes the last row.
    c = Screen(2, 4)
    c.feed(b"a\r\nb\r\nc")
    assert c.text() == "b\nc", repr(c.text())
    # Query answers.
    p = Screen(24, 80)
    p.feed(b"\x1b[3;7H")
    rep = answers(b"\x1b]11;?\x1b\\\x1b[6n\x1b]10;?\x07", None, p)
    assert rep == b"\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[3;7R\x1b]10;rgb:ffff/ffff/ffff\x1b\\", rep
    print("realscreen selftest ok")


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--cols", type=int, default=80)
    ap.add_argument("--rows", type=int, default=24)
    ap.add_argument("--cwd", help="directory to run the command in")
    ap.add_argument("--keys", default="", help="keys to type once the screen settles (Python escapes: \\r, \\x1b, \\t)")
    ap.add_argument("--xdg", help="XDG_CONFIG_HOME for the command (default: a throwaway directory)")
    ap.add_argument("--idle", type=float, default=1.5, help="seconds of silence that count as settled")
    ap.add_argument("--max-seconds", type=float, default=30.0)
    ap.add_argument("--selftest", action="store_true", help="run the interpreter on canned bytes and exit")
    ap.add_argument("cmd", nargs=argparse.REMAINDER, help="-- command and arguments")
    args = ap.parse_args()
    if args.selftest:
        selftest()
        return 0
    if args.cmd and args.cmd[0] == "--":
        args.cmd = args.cmd[1:]
    if not args.cmd:
        ap.error("give a command after --")
    print(run(args))
    return 0


if __name__ == "__main__":
    sys.exit(main())
