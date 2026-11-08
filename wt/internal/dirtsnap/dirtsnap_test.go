package dirtsnap

import (
	"os"
	"path/filepath"
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
