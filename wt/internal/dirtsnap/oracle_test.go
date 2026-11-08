package dirtsnap

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runIn is a Runner that runs git in dir with stdin.
func runIn(dir string) Runner {
	return func(stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		return cmd.Output()
	}
}

func gitT(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// oracleTree is git's own answer: HEAD read into a throwaway index, then
// `add -A` and `write-tree`.
func oracleTree(t *testing.T, dir string) string {
	t.Helper()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "idx")}
	gitT(t, dir, env, "read-tree", "HEAD")
	gitT(t, dir, env, "add", "-A")
	return gitT(t, dir, env, "write-tree")
}

// newRepo makes a fresh repository with one commit holding a.txt, x/y.txt and
// an executable run.sh.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, nil, "init", "-q")
	gitT(t, dir, nil, "config", "user.name", "t")
	gitT(t, dir, nil, "config", "user.email", "t@invalid")
	gitT(t, dir, nil, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n", 0o644)
	writeFile(t, filepath.Join(dir, "x", "y.txt"), "y\n", 0o644)
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\n", 0o755)
	gitT(t, dir, nil, "add", "-A")
	gitT(t, dir, nil, "commit", "-q", "-m", "init")
	return dir
}
