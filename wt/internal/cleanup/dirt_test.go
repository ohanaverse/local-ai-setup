package cleanup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/dirtsnap"
)

// dirtFixture is a linked worktree with a staged-then-edited file, an edited
// tracked file and an untracked file, so index and working tree both differ
// from HEAD and from each other.
type dirtFixture struct {
	f   *fixture
	dir string
}

func newDirtFixture(t *testing.T) dirtFixture {
	t.Helper()
	f := newFixture(t)
	f.Commit("a.txt", "a0", "a")
	f.Commit("b.txt", "b0", "b")
	f.Commit("s.txt", "s0", "s")
	dir := f.Worktree("dirty")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "a1")
	write("s.txt", "s1")
	gitIn(t, dir, "add", "s.txt")
	write("s.txt", "s2")
	write("u.txt", "u1")
	return dirtFixture{f: f, dir: dir}
}

func (d dirtFixture) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(d.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// record takes a Snapshot record of the worktree as it is now and returns it
// as the classifier would read it.
func (d dirtFixture) record(t *testing.T) RescueRecord {
	t.Helper()
	ref := d.f.addRecord(t, d.dir, recordOpts{Snapshot: true})
	recs, err := ListRecords(Git{Dir: d.dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Ref == ref {
			return r
		}
	}
	t.Fatalf("record %s not listed", ref)
	return RescueRecord{}
}

func (d dirtFixture) fact(t *testing.T) WorktreeFact {
	t.Helper()
	return WorktreeFact{Path: d.dir, Head: gitIn(t, d.dir, "rev-parse", "HEAD")}
}

func (d dirtFixture) covered(t *testing.T, r RescueRecord) bool {
	t.Helper()
	return dirtCovered(Git{Dir: d.dir}, d.fact(t), r)
}

// TestDirtCoveredMatrix pins which changes made after a rescue break the
// record's cover and which are the declared losses. A snapshot record is
// taken of a dirty worktree, one mutation follows, and the answer must be
// exactly the table's. The 0600 mode change and the empty directory are
// declared losses: a rescue's copy does not carry them (git records only the
// executable bit and no empty directory), so they still read as covered. This
// guards the rule that only byte, mode-bit, path or index differences count.
func TestDirtCoveredMatrix(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, d dirtFixture)
		covered bool
	}{
		{"none", func(*testing.T, dirtFixture) {}, true},
		{"edit tracked file", func(t *testing.T, d dirtFixture) { d.write(t, "a.txt", "a2") }, false},
		{"edit untracked file", func(t *testing.T, d dirtFixture) { d.write(t, "u.txt", "u2") }, false},
		{"new untracked file", func(t *testing.T, d dirtFixture) { d.write(t, "new.txt", "n") }, false},
		{"delete tracked file", func(t *testing.T, d dirtFixture) {
			if err := os.Remove(filepath.Join(d.dir, "b.txt")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"chmod +x", func(t *testing.T, d dirtFixture) {
			if err := os.Chmod(filepath.Join(d.dir, "a.txt"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"restage a tracked edit", func(t *testing.T, d dirtFixture) { gitIn(t, d.dir, "add", "a.txt") }, false},
		{"stage a new file", func(t *testing.T, d dirtFixture) { gitIn(t, d.dir, "add", "u.txt") }, false},
		{"replace file with symlink", func(t *testing.T, d dirtFixture) {
			p := filepath.Join(d.dir, "u.txt")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("a.txt", p); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"edit then revert to identical bytes", func(t *testing.T, d dirtFixture) {
			d.write(t, "a.txt", "changed")
			d.write(t, "a.txt", "a1")
		}, true},
		{"0600 mode change (declared loss)", func(t *testing.T, d dirtFixture) {
			if err := os.Chmod(filepath.Join(d.dir, "a.txt"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"empty directory added (declared loss)", func(t *testing.T, d dirtFixture) {
			if err := os.Mkdir(filepath.Join(d.dir, "emptydir"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newDirtFixture(t)
			r := d.record(t)
			c.mutate(t, d)
			if got := d.covered(t, r); got != c.covered {
				t.Fatalf("dirtCovered = %v, want %v", got, c.covered)
			}
		})
	}
}

// TestDirtCoveredNeverPreservedStates pins the states a record cannot vouch
// for even though index and working tree may still equal it: a hidden edit
// (skip-worktree or assume-unchanged, taken after the record, invisible to
// status), intent-to-add, an unmerged path, a nested repository and a changed
// submodule. Each must read as not covered, or removal could drop work the
// record never held.
func TestDirtCoveredNeverPreservedStates(t *testing.T) {
	t.Run("skip-worktree", func(t *testing.T) {
		d := newDirtFixture(t)
		r := d.record(t)
		gitIn(t, d.dir, "update-index", "--skip-worktree", "b.txt")
		d.write(t, "b.txt", "edited after the flag")
		if d.covered(t, r) {
			t.Fatal("covered with a skip-worktree edit")
		}
	})
	t.Run("assume-unchanged", func(t *testing.T) {
		d := newDirtFixture(t)
		r := d.record(t)
		gitIn(t, d.dir, "update-index", "--assume-unchanged", "b.txt")
		d.write(t, "b.txt", "edited after the flag")
		if d.covered(t, r) {
			t.Fatal("covered with an assume-unchanged edit")
		}
	})
	t.Run("intent-to-add", func(t *testing.T) {
		d := newDirtFixture(t)
		r := d.record(t)
		gitIn(t, d.dir, "add", "-N", "u.txt")
		if d.covered(t, r) {
			t.Fatal("covered with an intent-to-add entry")
		}
	})
	t.Run("unmerged path", func(t *testing.T) {
		d := newDirtFixture(t)
		r := d.record(t)
		// HEAD stays put, so only the unmerged entries can be the reason.
		blob := gitIn(t, d.dir, "rev-parse", "HEAD:a.txt")
		info := "0 0000000000000000000000000000000000000000\ta.txt\n" +
			"100644 " + blob + " 1\ta.txt\n100644 " + blob + " 2\ta.txt\n100644 " + blob + " 3\ta.txt\n"
		cmd := exec.Command("git", "-C", d.dir, "update-index", "--index-info")
		cmd.Stdin = strings.NewReader(info)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("update-index: %v\n%s", err, b)
		}
		if d.covered(t, r) {
			t.Fatal("covered with an unmerged path")
		}
	})
	t.Run("nested repository", func(t *testing.T) {
		d := newDirtFixture(t)
		nested := filepath.Join(d.dir, "inner")
		if err := os.Mkdir(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		gitIn(t, nested, "init", "-q")
		gitIn(t, nested, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "--allow-empty", "-qm", "x")
		r := d.record(t)
		if d.covered(t, r) {
			t.Fatal("covered with a nested repository")
		}
	})
	t.Run("changed submodule", func(t *testing.T) {
		d := newDirtFixture(t)
		sub := filepath.Join(t.TempDir(), "sub")
		gitIn(t, filepath.Dir(sub), "init", "-q", sub)
		gitIn(t, sub, "-c", "user.email=t@e", "-c", "user.name=T", "commit", "--allow-empty", "-qm", "x")
		gitIn(t, d.dir, "-c", "protocol.file.allow=always", "submodule", "add", sub, "sm")
		r := d.record(t)
		gitIn(t, filepath.Join(d.dir, "sm"), "-c", "user.email=t@e", "-c", "user.name=T", "commit", "--allow-empty", "-qm", "y")
		if d.covered(t, r) {
			t.Fatal("covered with a changed submodule")
		}
	})
}

// TestDirtCoveredRejectsForgedChain pins that a record with the right
// trailers but the wrong shape proves nothing: one whose W grandparent is not
// the source head, and one whose I and W are just the HEAD tree while the
// source is dirty. Both must read as not covered.
func TestDirtCoveredRejectsForgedChain(t *testing.T) {
	t.Run("grandparent is not the source head", func(t *testing.T) {
		d := newDirtFixture(t)
		other := d.f.Commit("other", "o", "other")
		ref := d.f.addRecord(t, d.dir, recordOpts{Snapshot: true})
		recs, err := ListRecords(Git{Dir: d.dir})
		if err != nil {
			t.Fatal(err)
		}
		r := recs[0]
		if r.Ref != ref {
			t.Fatalf("unexpected record %s", r.Ref)
		}
		// Same trailers, but the chain hangs off another commit.
		r.SourceHead = other
		w := d.fact(t)
		w.Head = other
		if dirtCovered(Git{Dir: d.dir}, w, r) {
			t.Fatal("covered by a chain whose grandparent is not the source head")
		}
	})
	t.Run("HEAD-tree record against a dirty source", func(t *testing.T) {
		d := newDirtFixture(t)
		ref := d.f.addRecord(t, d.dir, recordOpts{})
		recs, err := ListRecords(Git{Dir: d.dir})
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 1 || recs[0].Ref != ref {
			t.Fatalf("records = %+v", recs)
		}
		if d.covered(t, recs[0]) {
			t.Fatal("a HEAD-tree record covered a dirty worktree")
		}
	})
}

// TestDirtCoveredFailsClosed pins that every failure reads as not covered: a
// missing commit object, a cap lowered below the worktree's size, and git
// that cannot run in the directory. The positive control at the start shows
// each failure, not the fixture, is what flips the answer.
func TestDirtCoveredFailsClosed(t *testing.T) {
	t.Run("missing object", func(t *testing.T) {
		d := newDirtFixture(t)
		r := d.record(t)
		if !d.covered(t, r) {
			t.Fatal("control: not covered")
		}
		i := gitIn(t, d.dir, "rev-parse", r.Commit+"^")
		common := gitIn(t, d.dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err := os.Remove(filepath.Join(common, "objects", i[:2], i[2:])); err != nil {
			t.Fatal(err)
		}
		if d.covered(t, r) {
			t.Fatal("covered with the I commit missing")
		}
	})
	t.Run("cap lowered", func(t *testing.T) {
		d := newDirtFixture(t)
		r := d.record(t)
		old := dirtLimits
		dirtLimits = dirtsnap.Limits{MaxFiles: 1, MaxBytes: DirtMaxBytes}
		t.Cleanup(func() { dirtLimits = old })
		if d.covered(t, r) {
			t.Fatal("covered over the file cap")
		}
	})
	t.Run("failing git", func(t *testing.T) {
		d := newDirtFixture(t)
		r := d.record(t)
		w := d.fact(t)
		if dirtCovered(Git{Dir: filepath.Join(t.TempDir(), "missing")}, w, r) {
			t.Fatal("covered with git unable to run")
		}
	})
}

// TestDirtCoveredCheapChecksFirst pins that dirtCovered spends no git call
// when the worktree's head is not the record's source head: a counting git
// wrapper on PATH sees nothing. The same ordering keeps a clean row (the
// caller never reaches here) and a forged head from forking processes.
func TestDirtCoveredCheapChecksFirst(t *testing.T) {
	d := newDirtFixture(t)
	r := d.record(t)
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho x >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	w, ok := d.fact(t), d.fact(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	w.Head = strings.Repeat("0", 40)
	if dirtCovered(Git{Dir: d.dir}, w, r) {
		t.Fatal("covered with a different head")
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatalf("git was called %d times", strings.Count(string(b), "x"))
	}
	// Control: the wrapper does count a real check.
	if !dirtCovered(Git{Dir: d.dir}, ok, r) {
		t.Fatal("control: not covered")
	}
	if b, _ := os.ReadFile(log); len(b) == 0 {
		t.Fatal("control: wrapper saw no call")
	}
}

// TestDirtCoveredRacyCleanEntry pins a staged file edited again within the
// same timestamp as its index entry, with the same size: only the index's own
// mtime (racy-git) tells git to read the content. Why: a snapshot record's
// oracle once copied the index into a fresh file, whose newer mtime hid that
// edit, so W held the staged content and the check flaked about 1 run in 8.
// The oracle must see the edit, and the record must read as covered.
func TestDirtCoveredRacyCleanEntry(t *testing.T) {
	f := newFixture(t)
	f.Commit("s.txt", "s0", "s")
	dir := f.Worktree("racy")
	// ctime cannot be set; without this the edit's new ctime would give the
	// stat away and the race would not be forced.
	gitIn(t, dir, "config", "core.trustctime", "false")
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	p := filepath.Join(dir, "s.txt")
	set := func(path, content string) {
		t.Helper()
		if content != "" {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	set(p, "s1")
	gitIn(t, dir, "add", "s.txt")
	set(p, "s2")
	// The index now has the entry's mtime: racily clean, as git leaves it when
	// the edit lands in the second the index was written.
	// An empty content argument means only set the mtime (the index is not rewritten).
	set(gitIn(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "index"), "")
	_, wtree := snapshotTrees(t, dir)
	want := gitIn(t, dir, "hash-object", "s.txt")
	if got := gitIn(t, dir, "rev-parse", wtree+":s.txt"); got != want {
		t.Fatalf("oracle W has s.txt %s, want the working copy %s", got, want)
	}
	ref := f.addRecord(t, dir, recordOpts{Snapshot: true})
	recs, err := ListRecords(Git{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Ref == ref {
			if !dirtCovered(Git{Dir: dir}, WorktreeFact{Path: dir, Head: gitIn(t, dir, "rev-parse", "HEAD")}, r) {
				t.Fatal("racily clean edit: record not covered")
			}
			return
		}
	}
	t.Fatalf("record %s not listed", ref)
}

// rootDirt is a dirty primary checkout: a staged-then-edited file, an edited
// tracked file and an untracked file, with a Snapshot record taken of it.
func rootDirt(t *testing.T) (d dirtFixture, head, ref string) {
	t.Helper()
	d = newDirtFixture(t)
	root := d.f.Dir
	d.dir = root
	d.write(t, "a.txt", "a1")
	d.write(t, "s.txt", "s1")
	gitIn(t, root, "add", "s.txt")
	d.write(t, "s.txt", "s2")
	d.write(t, "u.txt", "u1")
	ref = d.f.addRecord(t, root, recordOpts{Snapshot: true})
	return d, gitIn(t, root, "rev-parse", "HEAD"), ref
}

// TestRecordCoversDirtOnPrimary pins that the exported check answers for the
// primary checkout the way dirtCovered does for a linked worktree: covered
// right after the record, and not covered after an edit, a new untracked
// file, a restage, an exec-bit flip or a hidden index flag; an edit reverted
// to identical bytes is still covered. Why: the root release gate trusts this
// answer as its last check before the root is reset.
func TestRecordCoversDirtOnPrimary(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, d dirtFixture)
		covered bool
	}{
		{"none", func(*testing.T, dirtFixture) {}, true},
		{"edit tracked file", func(t *testing.T, d dirtFixture) { d.write(t, "a.txt", "a2") }, false},
		{"extra untracked file", func(t *testing.T, d dirtFixture) { d.write(t, "new.txt", "n") }, false},
		{"restage", func(t *testing.T, d dirtFixture) { gitIn(t, d.dir, "add", "a.txt") }, false},
		{"exec bit flip", func(t *testing.T, d dirtFixture) {
			if err := os.Chmod(filepath.Join(d.dir, "a.txt"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"hidden index flag", func(t *testing.T, d dirtFixture) {
			gitIn(t, d.dir, "update-index", "--skip-worktree", "b.txt")
			d.write(t, "b.txt", "edited after the flag")
		}, false},
		{"edit then revert", func(t *testing.T, d dirtFixture) {
			d.write(t, "a.txt", "changed")
			d.write(t, "a.txt", "a1")
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, head, ref := rootDirt(t)
			c.mutate(t, d)
			got, err := RecordCoversDirt(Git{Dir: d.dir}, d.dir, head, ref)
			if err != nil || got != c.covered {
				t.Fatalf("RecordCoversDirt = %v, %v; want %v, nil", got, err, c.covered)
			}
		})
	}
}

// TestRecordCoversDirtUnknownRecord pins that a ref no record has is false
// (the error may be nil or set; the answer must be false either way). Why:
// a gate must never read a missing record as cover.
func TestRecordCoversDirtUnknownRecord(t *testing.T) {
	d, head, _ := rootDirt(t)
	got, err := RecordCoversDirt(Git{Dir: d.dir}, d.dir, head, RecordRefPrefix+"0123456789ab")
	if got {
		t.Fatalf("RecordCoversDirt = true, %v for an unknown record", err)
	}
}

// TestRecordCoversDirtWrongHead pins that a head other than the record's
// source head is not covered. Why: a record vouches only for the commit it
// was taken at.
func TestRecordCoversDirtWrongHead(t *testing.T) {
	d, _, ref := rootDirt(t)
	other := d.f.git("rev-parse", "HEAD~1")
	got, err := RecordCoversDirt(Git{Dir: d.dir}, d.dir, other, ref)
	if err != nil || got {
		t.Fatalf("RecordCoversDirt = %v, %v; want false, nil", got, err)
	}
}

// TestRecordCoversDirtDoesNotChangeClassification pins that the primary row
// still carries no Preserved after the export exists, in Gather and Rescan.
// Why: the root is never classified as preserved; only the release gate asks.
func TestRecordCoversDirtDoesNotChangeClassification(t *testing.T) {
	noGh(t)
	d, _, _ := rootDirt(t)
	s := gather(t, d.f)
	found := false
	for _, ws := range s.Worktrees {
		if ws.Fact.Primary {
			found = true
			if ws.Preserved != nil {
				t.Fatalf("Gather gave the primary Preserved = %+v", ws.Preserved)
			}
		}
	}
	if !found {
		t.Fatal("no primary worktree in the snapshot")
	}
	rep := Classify(s, Options{})
	for _, row := range rep.Rows {
		if row.Kind == KindWorktree && resolve(row.Path) == resolve(d.f.Dir) {
			if row.Preserved != nil {
				t.Fatalf("primary row Preserved = %+v", row.Preserved)
			}
			r2 := mustRescan(t, d.f, s, Options{}, targetOf(row))
			if r2.Preserved != nil {
				t.Fatalf("Rescan primary Preserved = %+v", r2.Preserved)
			}
			return
		}
	}
	t.Fatal("no primary row")
}
