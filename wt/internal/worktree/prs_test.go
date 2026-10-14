package worktree

import (
	"errors"
	"reflect"
	"testing"
)

// TestParsePRsMapsEachBranchToItsLatestPR pins the JSON-to-tag mapping the
// picker shows: one PR per head branch, the highest number winning when a
// branch has several, fork PRs skipped, an open draft reading "draft", and
// merged and closed reading as themselves. A regression shows a branch's old
// closed PR instead of its open one, or tags a local branch with a stranger's
// fork PR that happens to share its name.
func TestParsePRsMapsEachBranchToItsLatestPR(t *testing.T) {
	data := []byte(`[
		{"number": 383, "state": "OPEN", "isDraft": false, "headRefName": "fix/stop", "isCrossRepository": false, "title": "Stop waits"},
		{"number": 380, "state": "CLOSED", "isDraft": false, "headRefName": "fix/stop", "isCrossRepository": false, "title": "Old attempt"},
		{"number": 390, "state": "OPEN", "isDraft": false, "headRefName": "fix/stop", "isCrossRepository": true, "title": "Fork PR"},
		{"number": 382, "state": "MERGED", "isDraft": false, "headRefName": "fix/sync", "isCrossRepository": false, "title": "Sync waits"},
		{"number": 381, "state": "OPEN", "isDraft": true, "headRefName": "feat/draft", "isCrossRepository": false, "title": "WIP"},
		{"number": 379, "state": "CLOSED", "isDraft": false, "headRefName": "feat/dropped", "isCrossRepository": false, "title": "Dropped"},
		{"number": 378, "state": "OPEN", "isDraft": false, "headRefName": "only/fork", "isCrossRepository": true, "title": "Fork only"},
		{"number": 0, "state": "OPEN", "headRefName": "no/number"},
		{"number": 377, "state": "OPEN", "headRefName": ""}
	]`)
	got, err := parsePRs(data)
	if err != nil {
		t.Fatalf("parsePRs: %v", err)
	}
	want := map[string]PR{
		"fix/stop":     {Number: 383, State: PROpen, Title: "Stop waits"},
		"fix/sync":     {Number: 382, State: PRMerged, Title: "Sync waits"},
		"feat/draft":   {Number: 381, State: PRDraft, Title: "WIP"},
		"feat/dropped": {Number: 379, State: PRClosed, Title: "Dropped"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePRs =\n%#v\nwant\n%#v", got, want)
	}
}

// TestParsePRsKeepsTheHighestNumberWhateverTheOrder pins that the choice
// between a branch's PRs does not depend on the order gh lists them in.
// Without it a re-ordered listing would flip a branch's tag between runs.
func TestParsePRsKeepsTheHighestNumberWhateverTheOrder(t *testing.T) {
	got, err := parsePRs([]byte(`[
		{"number": 10, "state": "CLOSED", "headRefName": "b"},
		{"number": 12, "state": "OPEN", "headRefName": "b"},
		{"number": 11, "state": "MERGED", "headRefName": "b"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if got["b"].Number != 12 || got["b"].State != PROpen {
		t.Errorf("b = %+v, want #12 open", got["b"])
	}
}

// TestPRsFailureIsAnEmptyMap pins the silent fallback: gh missing, failing,
// or printing something that is not the expected JSON leaves the picker with
// no tags and no error to show. A regression would surface a gh error (or
// panic on a nil map) in a picker that worked fine before PR tags existed.
func TestPRsFailureIsAnEmptyMap(t *testing.T) {
	for name, fn := range map[string]func(string, string) ([]byte, error){
		"gh fails": func(string, string) ([]byte, error) {
			return nil, errors.New("exec: \"gh\": executable file not found in $PATH")
		},
		"bad json":    func(string, string) ([]byte, error) { return []byte("not json"), nil },
		"wrong shape": func(string, string) ([]byte, error) { return []byte(`{"number": 1}`), nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(StubPRListForTest(fn))
			got := PRs("/repo")
			if got == nil || len(got) != 0 {
				t.Errorf("PRs = %#v, want an empty, non-nil map", got)
			}
		})
	}
}

// TestPRsRunsInTheRepoDir pins that both listings run in the directory the
// picker enumerated, open PRs first, so gh finds that repository's remote
// whatever wt's own working directory is.
func TestPRsRunsInTheRepoDir(t *testing.T) {
	var calls []string
	t.Cleanup(StubPRListForTest(func(dir, state string) ([]byte, error) {
		calls = append(calls, dir+" "+state)
		return []byte(`[{"number": 5, "state": "OPEN", "headRefName": "x"}]`), nil
	}))
	got := PRs("/repo")
	if want := []string{"/repo open", "/repo all"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if got["x"].Number != 5 {
		t.Errorf("x = %+v, want #5", got["x"])
	}
}

// TestPRsKeepsAnOldOpenPRTheAllListingDropped pins why there are two
// listings: an open PR older than the newest prLimit PRs of every state is
// missing from the all-states listing but still in the open one, and its
// branch keeps its tag. A regression back to one listing loses the tag on
// exactly the long-lived open PRs.
func TestPRsKeepsAnOldOpenPRTheAllListingDropped(t *testing.T) {
	t.Cleanup(StubPRListForTest(func(_, state string) ([]byte, error) {
		if state == "open" {
			return []byte(`[{"number": 7, "state": "OPEN", "headRefName": "old"}]`), nil
		}
		return []byte(`[{"number": 900, "state": "MERGED", "headRefName": "new"}]`), nil
	}))
	got := PRs("/repo")
	if got["old"].Number != 7 || got["old"].State != PROpen {
		t.Errorf("old = %+v, want #7 open", got["old"])
	}
	if got["new"].Number != 900 || got["new"].State != PRMerged {
		t.Errorf("new = %+v, want #900 merged", got["new"])
	}
}

// TestPRsOneFailedListingKeepsTheOther pins that the two listings fail
// independently: a timeout or bad output from one still leaves the other's
// tags on screen instead of none.
func TestPRsOneFailedListingKeepsTheOther(t *testing.T) {
	for _, failing := range []string{"open", "all"} {
		t.Run(failing+" fails", func(t *testing.T) {
			t.Cleanup(StubPRListForTest(func(_, state string) ([]byte, error) {
				if state == failing {
					return []byte("not json"), nil
				}
				return []byte(`[{"number": 3, "state": "OPEN", "headRefName": "b"}]`), nil
			}))
			if got := PRs("/repo"); got["b"].Number != 3 {
				t.Errorf("PRs = %#v, want b -> #3", got)
			}
		})
	}
}

// TestRealListPRsRefusesInATestBinary pins the second guard behind the
// TestMain stub: a test in any package that reaches the real gh call (one
// whose TestMain forgot the stub) gets an error instead of running the
// developer's gh against GitHub.
func TestRealListPRsRefusesInATestBinary(t *testing.T) {
	if _, err := realListPRs(t.TempDir(), "all"); !errors.Is(err, errPRListUnderTest) {
		t.Errorf("realListPRs err = %v, want errPRListUnderTest", err)
	}
}

// TestPRBranchStripsTheRemote pins the lookup key: a remote-tracking row's
// PR is found under the branch name without its remote, a local branch keeps
// a slash in its name, and a detached worktree has no PR. A regression tags
// no remote branch at all, or strips "feat/" off a local branch and finds
// the wrong PR.
func TestPRBranchStripsTheRemote(t *testing.T) {
	cases := []struct {
		kind GroupKind
		e    Entry
		want string
	}{
		{GroupRemoteBranches, Entry{Branch: "origin/fix/stop"}, "fix/stop"},
		{GroupLocalBranches, Entry{Branch: "fix/stop"}, "fix/stop"},
		{GroupWorktrees, Entry{Branch: "feat/x", Path: "/w"}, "feat/x"},
		{GroupWorktrees, Entry{Branch: "(detached)", Path: "/w"}, ""},
	}
	for _, c := range cases {
		if got := PRBranch(c.kind, c.e); got != c.want {
			t.Errorf("PRBranch(%v, %q) = %q, want %q", c.kind, c.e.Branch, got, c.want)
		}
	}
}
