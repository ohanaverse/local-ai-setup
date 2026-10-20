package worktree

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestRepoFromURLForms(t *testing.T) {
	cases := map[string]string{
		"https://github.com/Owner/Repo":                        "owner/repo",
		"https://github.com/owner/repo.git":                    "owner/repo",
		"ssh://git@github.com/owner/repo.git":                  "owner/repo",
		"git@github.com:Owner/Repo.git":                        "owner/repo",
		"https://github.com/Owner/Repo.git/":                   "owner/repo",
		"file:///Users/keith/github/Ohanaverse/local-ai-setup": "",
		"file:///srv/repo.git":                                 "",
		"/srv/repo.git":                                        "",
		"":                                                     "",
	}
	for in, want := range cases {
		if got := repoFromURL(in); got != want {
			t.Errorf("repoFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitRemoteBranchLongestRemoteWins(t *testing.T) {
	remotes := map[string]string{"up": "a/b", "up/stream": "c/d"}
	r, b, ok := SplitRemoteBranch("up/stream/feat/x", remotes)
	if !ok || r != "up/stream" || b != "feat/x" {
		t.Errorf("got (%q, %q, %v), want (up/stream, feat/x, true)", r, b, ok)
	}
	r, b, ok = SplitRemoteBranch("up/feat/x", remotes)
	if !ok || r != "up" || b != "feat/x" {
		t.Errorf("got (%q, %q, %v), want (up, feat/x, true)", r, b, ok)
	}
	if r, b, ok = SplitRemoteBranch("gone/x", remotes); ok || r != "" || b != "" {
		t.Errorf("unknown remote: got (%q, %q, %v), want empty, false", r, b, ok)
	}
}

func TestRemoteReposReadsEachRemoteURL(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	for _, args := range [][]string{
		{"remote", "add", "origin", "https://github.com/Base/Repo.git"},
		{"remote", "add", "keith", "gh:keith/fork"},
		{"remote", "add", "local", "/srv/repo.git"},
		{"config", "url.https://github.com/.insteadOf", "gh:"},
	} {
		if out, err := runGitCmd(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	want := map[string]string{"origin": "base/repo", "keith": "keith/fork", "local": ""}
	if got := RemoteRepos(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("RemoteRepos = %v, want %v", got, want)
	}
	if got := RemoteRepos(filepath.Join(dir, "nope")); got == nil || len(got) != 0 {
		t.Errorf("RemoteRepos on a non-repo = %v, want empty non-nil map", got)
	}
}

func remoteEntry(groups []EntryGroup, branch string) (Entry, bool) {
	for _, g := range groups {
		if g.Kind != GroupRemoteBranches {
			continue
		}
		for _, e := range g.Entries {
			if e.Branch == branch {
				return e, true
			}
		}
	}
	return Entry{}, false
}

func TestEnumerateFillsRemoteRepoAndBranchOnRemoteEntries(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	setupRemote(t, dir, "feat-x", "feat/x")
	if out, err := runGitCmd(dir, "remote", "set-url", "origin", "git@github.com:Keith/Fork.git"); err != nil {
		t.Fatalf("set-url: %v\n%s", err, out)
	}
	// Enumerate fetches every remote, and origin now names a real GitHub
	// repo: make any ssh the fetch starts fail at once so the test never
	// reaches the developer's account or the network.
	t.Setenv("GIT_SSH_COMMAND", "false")
	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := remoteEntry(groups, "origin/feat/x")
	if !ok {
		t.Fatalf("origin/feat/x missing: %+v", groups)
	}
	if e.RemoteRepo != "keith/fork" || e.RemoteBranch != "feat/x" {
		t.Errorf("RemoteRepo/RemoteBranch = %q/%q, want keith/fork / feat/x", e.RemoteRepo, e.RemoteBranch)
	}
}

func TestEnumerateLeavesAStaleRemoteRefUntagged(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	if out, err := runGitCmd(dir, "update-ref", "refs/remotes/gone/x", "HEAD"); err != nil {
		t.Fatalf("update-ref: %v\n%s", err, out)
	}
	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := remoteEntry(groups, "gone/x")
	if !ok {
		t.Fatalf("gone/x missing: %+v", groups)
	}
	if e.RemoteRepo != "" || e.RemoteBranch != "" {
		t.Errorf("stale ref tagged %q/%q, want empty", e.RemoteRepo, e.RemoteBranch)
	}
}
