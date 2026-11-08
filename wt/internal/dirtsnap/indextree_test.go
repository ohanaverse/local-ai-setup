package dirtsnap

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// indexOracle is git's own answer for the index: its bytes copied into a
// throwaway GIT_INDEX_FILE, then `write-tree` there (the copy takes the
// cache-tree rewrite, the real index does not).
func indexOracle(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	idx := filepath.Join(t.TempDir(), "idx")
	if err := os.WriteFile(idx, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return gitT(t, dir, []string{"GIT_INDEX_FILE=" + idx}, "write-tree")
}

// TestIndexTreeMatchesWriteTree compares IndexTree with git's write-tree on a
// copy of the index across clean, staged add/delete/mode-flip, nested, odd
// names and a gitlink, and proves the real index bytes stay as found. A wrong
// tree would be a false record, so equality with git is the whole contract.
func TestIndexTreeMatchesWriteTree(t *testing.T) {
	cases := map[string]func(t *testing.T, d string){
		"clean": func(t *testing.T, d string) {},
		"staged add": func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "n", "m", "new.txt"), "n", 0o644)
			gitT(t, d, nil, "add", "n")
		},
		"staged delete": func(t *testing.T, d string) { gitT(t, d, nil, "rm", "-q", "x/y.txt") },
		"staged mode flip": func(t *testing.T, d string) {
			gitT(t, d, nil, "update-index", "--chmod=-x", "run.sh")
		},
		"odd names": func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "sp ace.txt"), "s", 0o644)
			writeFile(t, filepath.Join(d, "café.txt"), "c", 0o644)
			writeFile(t, filepath.Join(d, "a\nb.txt"), "l", 0o644)
			writeFile(t, filepath.Join(d, "a.b"), "d", 0o644)
			writeFile(t, filepath.Join(d, "a", "z"), "z", 0o644)
			gitT(t, d, nil, "add", "-A")
		},
		"gitlink": func(t *testing.T, d string) {
			gitT(t, d, nil, "update-index", "--add", "--cacheinfo",
				"160000,"+gitT(t, d, nil, "rev-parse", "HEAD")+",sub")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			d := newRepo(t)
			setup(t, d)
			before, _ := os.ReadFile(filepath.Join(d, ".git", "index"))
			got, err := IndexTree(d, runIn(d), false)
			if err != nil {
				t.Fatal(err)
			}
			if want := indexOracle(t, d); got != want {
				t.Errorf("IndexTree = %s, want %s", got, want)
			}
			after, _ := os.ReadFile(filepath.Join(d, ".git", "index"))
			if string(before) != string(after) {
				t.Error("the index file changed")
			}
		})
	}
}

// TestIndexTreeDiffersFromWriteTreeOnIntentToAdd pins the documented gap: an
// intent-to-add entry (`git add -N`) is listed by `ls-files --stage` as a
// stage-0 empty blob, so IndexTree records it while write-tree leaves it out.
// Why: the doc tells callers to refuse i-t-a first (rescue's Preflight does);
// if git or IndexTree ever changes here, this test says the doc is stale.
func TestIndexTreeDiffersFromWriteTreeOnIntentToAdd(t *testing.T) {
	d := newRepo(t)
	writeFile(t, filepath.Join(d, "ita.txt"), "later\n", 0o644)
	gitT(t, d, nil, "add", "-N", "ita.txt")
	got, err := IndexTree(d, runIn(d), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := indexOracle(t, d); got == want {
		t.Fatalf("IndexTree = write-tree = %s for an intent-to-add entry; update IndexTree's doc", got)
	}
}

// TestIndexTreeRefusesAnUnmergedIndex pins ErrUnsupported for a conflict: a
// tree cannot describe stages 1 to 3.
func TestIndexTreeRefusesAnUnmergedIndex(t *testing.T) {
	d := newRepo(t)
	gitT(t, d, nil, "checkout", "-q", "-b", "other")
	writeFile(t, filepath.Join(d, "a.txt"), "other\n", 0o644)
	gitT(t, d, nil, "commit", "-qam", "other")
	gitT(t, d, nil, "checkout", "-q", "-")
	writeFile(t, filepath.Join(d, "a.txt"), "main\n", 0o644)
	gitT(t, d, nil, "commit", "-qam", "main")
	cmd := execIn(d, "merge", "other")
	_ = cmd.Run() // conflicts: non-zero is expected
	if _, err := IndexTree(d, runIn(d), false); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// TestIndexTreeWriteModes proves write=false stores nothing and write=true
// stores a tree git can read: a staged new file makes a tree git does not
// have yet.
func TestIndexTreeWriteModes(t *testing.T) {
	d := newRepo(t)
	writeFile(t, filepath.Join(d, "q", "fresh.txt"), "fresh-content\n", 0o644)
	gitT(t, d, nil, "add", "q")
	// Objects of a staged file exist already; only the new trees are at stake.
	id, err := IndexTree(d, runIn(d), false)
	if err != nil {
		t.Fatal(err)
	}
	if execIn(d, "cat-file", "-e", id).Run() == nil {
		t.Fatal("write=false stored the tree")
	}
	id2, err := IndexTree(d, runIn(d), true)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("ids differ: %s vs %s", id, id2)
	}
	if err := execIn(d, "cat-file", "-e", id).Run(); err != nil {
		t.Fatalf("write=true tree unreadable: %v", err)
	}
}

func execIn(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}
