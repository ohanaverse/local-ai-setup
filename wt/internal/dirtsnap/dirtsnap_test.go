package dirtsnap

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// TestBuildOfACleanTreeIsHeadsTree pins that a repository with no working
// changes yields HEAD's own tree id, so a clean source adds nothing.
func TestBuildOfACleanTreeIsHeadsTree(t *testing.T) {
	dir := newRepo(t)
	got, err := Build(dir, runIn(dir), true, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if want := gitT(t, dir, nil, "rev-parse", "HEAD^{tree}"); got != want {
		t.Fatalf("Build = %s, want HEAD tree %s", got, want)
	}
}

// TestBuildMatchesTheTempIndexOracle pins that for each kind of regular-file
// change Build names the same tree as git's own temp-index route
// (read-tree, add -A, write-tree), which is the definition of correct.
func TestBuildMatchesTheTempIndexOracle(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{"modified tracked file", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "a.txt"), "changed\n", 0o644)
		}},
		{"new top-level untracked file", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "new.txt"), "n\n", 0o644)
		}},
		{"tracked file deleted from disk", func(t *testing.T, d string) {
			if err := os.Remove(filepath.Join(d, "a.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"exec bit flipped", func(t *testing.T, d string) {
			if err := os.Chmod(filepath.Join(d, "run.sh"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"new nested untracked directory", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "n", "m", "o.txt"), "o\n", 0o644)
		}},
		{"staged-only change", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "a.txt"), "staged\n", 0o644)
			gitT(t, d, nil, "add", "a.txt")
		}},
		{"entire tracked directory deleted", func(t *testing.T, d string) {
			if err := os.RemoveAll(filepath.Join(d, "x")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			tc.mutate(t, dir)
			got, err := Build(dir, runIn(dir), true, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if want := oracleTree(t, dir); got != want {
				t.Fatalf("Build = %s, oracle = %s", got, want)
			}
		})
	}
}

// TestBuildMatchesTheOracleOnTrickyShapes pins the shapes a plain add/remove
// walk gets wrong (type changes, link entries, sibling ordering, odd names,
// ignore rules, file modes) against git's own temp-index answer.
func TestBuildMatchesTheOracleOnTrickyShapes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{"tracked file replaced by a directory", func(t *testing.T, d string) {
			rm(t, filepath.Join(d, "a.txt"))
			writeFile(t, filepath.Join(d, "a.txt", "inner.txt"), "i\n", 0o644)
		}},
		{"tracked directory replaced by a file", func(t *testing.T, d string) {
			rm(t, filepath.Join(d, "x"))
			writeFile(t, filepath.Join(d, "x"), "now a file\n", 0o644)
		}},
		{"new symlink", func(t *testing.T, d string) {
			if err := os.Symlink("a.txt", filepath.Join(d, "ln")); err != nil {
				t.Fatal(err)
			}
		}},
		{"changed symlink", func(t *testing.T, d string) {
			if err := os.Symlink("a.txt", filepath.Join(d, "ln")); err != nil {
				t.Fatal(err)
			}
			gitT(t, d, nil, "add", "ln")
			gitT(t, d, nil, "commit", "-q", "-m", "ln")
			rm(t, filepath.Join(d, "ln"))
			if err := os.Symlink("x/y.txt", filepath.Join(d, "ln")); err != nil {
				t.Fatal(err)
			}
		}},
		{"file replaced by a symlink", func(t *testing.T, d string) {
			rm(t, filepath.Join(d, "a.txt"))
			if err := os.Symlink("run.sh", filepath.Join(d, "a.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"siblings s, s.b, s0 and x.a beside directory x/", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "s"), "s\n", 0o644)
			writeFile(t, filepath.Join(d, "s.b"), "sb\n", 0o644)
			writeFile(t, filepath.Join(d, "s-dir", "f"), "f\n", 0o644)
			writeFile(t, filepath.Join(d, "s0"), "s0\n", 0o644)
			writeFile(t, filepath.Join(d, "x.a"), "xa\n", 0o644)
			writeFile(t, filepath.Join(d, "x", "z"), "z\n", 0o644)
		}},
		{"spaces and non-ASCII in a name", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "sp ace", "café 日本.txt"), "u\n", 0o644)
		}},
		{"newline in a name", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "two\nlines.txt"), "n\n", 0o644)
		}},
		{"ignored file and directory are absent", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, ".gitignore"), "*.log\nbuild/\n", 0o644)
			writeFile(t, filepath.Join(d, "debug.log"), "l\n", 0o644)
			writeFile(t, filepath.Join(d, "build", "out.bin"), "b\n", 0o644)
			writeFile(t, filepath.Join(d, "keep.txt"), "k\n", 0o644)
		}},
		{"mode 0600 untracked file", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "secret"), "s\n", 0o600)
		}},
		{"empty untracked file", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "empty"), "", 0o644)
		}},
		{"tracked file emptied", func(t *testing.T, d string) {
			writeFile(t, filepath.Join(d, "a.txt"), "", 0o644)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			tc.mutate(t, dir)
			got, err := Build(dir, runIn(dir), true, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if want := oracleTree(t, dir); got != want {
				t.Fatalf("Build = %s, oracle = %s", got, want)
			}
		})
	}
}

func rm(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

// TestBuildWriteFalseWritesNothing pins that write=false stores no object (a
// dry run must not touch the object store) and write=true stores the blob and
// the tree it names.
func TestBuildWriteFalseWritesNothing(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, filepath.Join(dir, "fresh.txt"), "never stored before\n", 0o644)
	blob := gitT(t, dir, nil, "hash-object", "fresh.txt")
	exists := func(id string) bool {
		return exec.Command("git", "-C", dir, "cat-file", "-e", id).Run() == nil
	}
	tree, err := Build(dir, runIn(dir), false, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if exists(blob) || exists(tree) {
		t.Fatalf("write=false stored objects (blob %v, tree %v)", exists(blob), exists(tree))
	}
	tree2, err := Build(dir, runIn(dir), true, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if tree2 != tree {
		t.Fatalf("write changed the id: %s vs %s", tree2, tree)
	}
	if !exists(blob) || !exists(tree) {
		t.Fatalf("write=true missed objects (blob %v, tree %v)", exists(blob), exists(tree))
	}
}

// TestBuildRefusesWhatATreeCannotHold pins ErrUnsupported for a nested
// repository, a submodule gitlink (present or deleted) and a fifo, none of
// which a plain blob tree can describe.
func TestBuildRefusesWhatATreeCannotHold(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{"nested repository", func(t *testing.T, d string) {
			n := filepath.Join(d, "nested")
			if err := os.MkdirAll(n, 0o755); err != nil {
				t.Fatal(err)
			}
			gitT(t, n, nil, "init", "-q")
			writeFile(t, filepath.Join(n, "f"), "f\n", 0o644)
		}},
		{"submodule gitlink", func(t *testing.T, d string) {
			addGitlink(t, d)
			writeFile(t, filepath.Join(d, "sub", "f"), "dirty\n", 0o644)
		}},
		{"submodule gitlink deleted from disk", func(t *testing.T, d string) {
			addGitlink(t, d)
			rm(t, filepath.Join(d, "sub"))
		}},
		{"fifo", func(t *testing.T, d string) {
			// git status hides an untracked fifo, so replace a tracked file.
			rm(t, filepath.Join(d, "a.txt"))
			if err := syscall.Mkfifo(filepath.Join(d, "a.txt"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			tc.mutate(t, dir)
			_, err := Build(dir, runIn(dir), false, Limits{})
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
		})
	}
}

// addGitlink commits a gitlink at sub/ without a .gitmodules file, using a
// real inner repository's HEAD.
func addGitlink(t *testing.T, d string) {
	t.Helper()
	sub := filepath.Join(d, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, sub, nil, "init", "-q")
	gitT(t, sub, nil, "config", "user.name", "t")
	gitT(t, sub, nil, "config", "user.email", "t@invalid")
	gitT(t, sub, nil, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(sub, "f"), "f\n", 0o644)
	gitT(t, sub, nil, "add", "-A")
	gitT(t, sub, nil, "commit", "-q", "-m", "s")
	gitT(t, d, nil, "add", "sub")
	gitT(t, d, nil, "commit", "-q", "-m", "sub")
}

// TestBuildStopsAtTheLimits pins ErrTooLarge for too many changed files and
// too many changed bytes, and that zero Limits mean unlimited.
func TestBuildStopsAtTheLimits(t *testing.T) {
	dir := newRepo(t)
	for _, n := range []string{"one", "two", "three"} {
		writeFile(t, filepath.Join(dir, n), "x\n", 0o644)
	}
	if _, err := Build(dir, runIn(dir), false, Limits{MaxFiles: 2}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("MaxFiles err = %v, want ErrTooLarge", err)
	}
	if _, err := Build(dir, runIn(dir), false, Limits{MaxFiles: 3}); err != nil {
		t.Fatalf("MaxFiles at the count: %v", err)
	}
	if _, err := Build(dir, runIn(dir), false, Limits{}); err != nil {
		t.Fatalf("zero Limits: %v", err)
	}

	dir = newRepo(t)
	writeFile(t, filepath.Join(dir, "big"), "0123456789", 0o644)
	if _, err := Build(dir, runIn(dir), false, Limits{MaxBytes: 5}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("MaxBytes err = %v, want ErrTooLarge", err)
	}
	if _, err := Build(dir, runIn(dir), false, Limits{MaxBytes: 10}); err != nil {
		t.Fatalf("MaxBytes at the size: %v", err)
	}
}
