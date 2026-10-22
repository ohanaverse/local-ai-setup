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

// TestEnumerateReposReturnsEveryRemoteWithoutCase pins the set the picker
// hands to a local row's PR lookup, read in the same pass as the rows: a
// repository with no remotes gives an empty set that matches nothing (never
// a panic), and every remote's owner/repo is in it, matched without case. A
// regression either tags local rows from a repository that is not yours or
// drops your fork's PR when its URL's case differs from gh's.
func TestEnumerateReposReturnsEveryRemoteWithoutCase(t *testing.T) {
	bare := t.TempDir()
	gitInit(t, bare)
	_, set, err := EnumerateRepos(bare, bare)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 0 {
		t.Errorf("no remotes: set = %v, want an empty set that matches nothing", set)
	}

	dir := t.TempDir()
	gitInit(t, dir)
	for _, args := range [][]string{
		{"remote", "add", "origin", "https://github.com/Base/Repo.git"},
		{"remote", "add", "fork", "git@github.com:me/Fork.git"},
	} {
		if out, err := runGitCmd(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	_, set, err = EnumerateRepos(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !set["base/repo"] || !set["me/fork"] || len(set) != 2 {
		t.Errorf("set = %v, want exactly base/repo and me/fork", set)
	}
}

// TestEnumerateReposTagsLocalRowsWithTheirPushRepository pins Entry.TrackRepo:
// a branch pushes to its pushRemote, else tracks its upstream's remote, and a
// branch with neither (or a remote naming no repository) has none. A
// regression leaves the local-row PR lookup guessing among every remote.
func TestEnumerateReposTagsLocalRowsWithTheirPushRepository(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	for _, args := range [][]string{
		{"remote", "add", "origin", "https://github.com/base/repo.git"},
		{"remote", "add", "fork", "git@github.com:me/fork.git"},
		{"branch", "tracked"}, {"branch", "pushed"}, {"branch", "bare"},
		{"update-ref", "refs/remotes/origin/tracked", "HEAD"},
		{"config", "branch.tracked.remote", "origin"},
		{"config", "branch.tracked.merge", "refs/heads/tracked"},
		{"config", "branch.pushed.remote", "origin"},
		{"config", "branch.pushed.merge", "refs/heads/pushed"},
		{"config", "branch.pushed.pushRemote", "fork"},
	} {
		if out, err := runGitCmd(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	groups, _, err := EnumerateRepos(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, g := range groups {
		if g.Kind != GroupRemoteBranches {
			for _, e := range g.Entries {
				got[e.Branch] = e.TrackRepo
			}
		}
	}
	for branch, want := range map[string]string{"tracked": "base/repo", "pushed": "me/fork", "bare": ""} {
		if got[branch] != want {
			t.Errorf("%s: TrackRepo = %q, want %q", branch, got[branch], want)
		}
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

func TestRemoteReposListsEveryRemoteInOneCall(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	for _, args := range [][]string{
		{"remote", "add", "a", "https://github.com/o/a.git"},
		{"remote", "add", "b/c", "https://github.com/o/b.git"},
		{"remote", "set-url", "--push", "a", "https://github.com/o/push-only.git"},
	} {
		if out, err := runGitCmd(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	want := map[string]string{"a": "o/a", "b/c": "o/b"}
	if got := RemoteRepos(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("RemoteRepos = %v, want %v (the fetch URL, not the push URL)", got, want)
	}
}
