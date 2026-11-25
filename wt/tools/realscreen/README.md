# realscreen

A stdlib-only Python driver that shows what a TUI really draws at a fixed
terminal size. A unit test of a view can pass while the real 80-column screen
cuts a line short, so a TUI change gets a real-screen check
([testing.md](../../docs/internals/testing.md)).

It runs a command under a pty with `TIOCSWINSZ` set before exec, answers the
OSC 10/11/12 colour queries and the cursor-position query (without the answers
a bubbletea program draws nothing), rebuilds the screen from the bytes with a
small VT100 interpreter (cursor moves, `CSI H/J/K`, alt screen; colours are
ignored), types `--keys` once the screen settles, sends ctrl+c, kills and waits
the command's whole process group, and prints the settled screen as text.

## The 80x24 rule

Check every screen you change at **80x24** (the defaults), and the narrow sizes
the layout tests name where the change affects them (`--cols 40 --rows 12`).
A line that fits in a wider terminal is a bug here.

## Use

```bash
# A scratch repository with one linked worktree, a throwaway binary.
D=$(mktemp -d)
go build -o "$D/wt" ./cmd/wt-session            # from wt/
R=$(mktemp -d)
git -C "$R" init -q -b main
git -C "$R" -c user.name=T -c user.email=t@example.com commit -q --allow-empty -m init
git -C "$R" worktree add -q -b topic "$R/.worktrees/topic"

python3 -I tools/realscreen/realscreen.py --cwd "$R" -- "$D/wt" worktrees
python3 -I tools/realscreen/realscreen.py --cwd "$R" --keys 'i' -- "$D/wt" worktrees   # press i first
```

| Flag | Meaning |
| --- | --- |
| `--cols N --rows N` | Terminal size (default 80x24). |
| `--cwd DIR` | Directory to run the command in. |
| `--keys "..."` | Typed once the screen has settled; Python escapes such as `\r`, `\t`, `\x1b`. |
| `--xdg DIR` | `XDG_CONFIG_HOME` for the command. Default: a throwaway directory, removed afterwards, so the run never reads or writes your real config. |
| `--idle S` | Seconds of silence that count as settled (default 1.5). |
| `--max-seconds S` | Hard stop (default 30). |

Run it against a scratch repository, never one you care about: the TUI is the
real program and `--keys` can confirm a removal. Children the command starts
(a held-claim `sleep`, say) are killed and waited, but check `pgrep` after a
run that you interrupted.

## Check the driver itself

```bash
python3 -I tools/realscreen/realscreen.py --selftest
```

This runs the interpreter on canned bytes (alt screen, cursor moves, erase,
wrap, scroll, split UTF-8, query answers) and needs no TUI.
