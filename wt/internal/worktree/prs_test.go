package worktree

import (
	"errors"
	"reflect"
	"testing"
)

// TestParsePRsMapsEachBranchToItsLatestPR pins the JSON-to-tag mapping the
// picker shows: one PR per head branch, an open (or draft) PR winning over
// a closed or merged one and the highest number winning otherwise, fork PRs
// skipped, an open draft reading "draft", and
// merged and closed reading as themselves. A regression shows a branch's old
// closed PR instead of its open one — even when the closed one is newer — or tags a local branch with a stranger's
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
		{"number": 300, "state": "CLOSED", "isDraft": false, "headRefName": "fix/dup", "isCrossRepository": false, "title": "Duplicate"},
		{"number": 50, "state": "OPEN", "isDraft": true, "headRefName": "fix/dup", "isCrossRepository": false, "title": "Live"},
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
		"fix/dup":      {Number: 50, State: PRDraft, Title: "Live"},
	}
	if !reflect.DeepEqual(got.ByBranch, want) {
		t.Errorf("parsePRs =\n%#v\nwant\n%#v", got.ByBranch, want)
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
	if got.ByBranch["b"].Number != 12 || got.ByBranch["b"].State != PROpen {
		t.Errorf("b = %+v, want #12 open", got.ByBranch["b"])
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
			got := PRs("/repo", PRListAll)
			if !got.Loaded() || len(got.ByBranch) != 0 {
				t.Errorf("PRs = %#v, want a loaded, empty set", got)
			}
		})
	}
}

// TestPRsRunsInTheRepoDir pins that a listing runs once, for the state asked
// for, in the directory the picker enumerated, so gh finds that
// repository's remote whatever wt's own working directory is.
func TestPRsRunsInTheRepoDir(t *testing.T) {
	var calls []string
	t.Cleanup(StubPRListForTest(func(dir, state string) ([]byte, error) {
		calls = append(calls, dir+" "+state)
		return []byte(`[{"number": 5, "state": "OPEN", "headRefName": "x"}]`), nil
	}))
	got := PRs("/repo", PRListOpen)
	if want := []string{"/repo open"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if got.ByBranch["x"].Number != 5 {
		t.Errorf("x = %+v, want #5", got.ByBranch["x"])
	}
}

// TestMergePRsPrefersOpenThenTheHigherNumberInEitherOrder pins how the
// picker joins its two listings: every branch of both, and a branch in both
// keeps its open PR over a closed or merged one, else the higher-numbered
// PR, whichever listing came first — the same rule as within one listing. A
// regression lets the slower listing's older PR overwrite a newer one, lets
// a newer closed duplicate hide the branch's open PR, or drops an old open
// PR only the open listing had.
func TestMergePRsPrefersOpenThenTheHigherNumberInEitherOrder(t *testing.T) {
	open := map[string]PR{
		"old": {Number: 7, State: PROpen},
		"b":   {Number: 10, State: PROpen},
		"c":   {Number: 20, State: PRDraft},
	}
	all := map[string]PR{
		"new": {Number: 900, State: PRMerged},
		"b":   {Number: 12, State: PRClosed},
		"c":   {Number: 21, State: PROpen},
	}
	want := map[string]PR{
		"old": {Number: 7, State: PROpen},
		"new": {Number: 900, State: PRMerged},
		"b":   {Number: 10, State: PROpen},
		"c":   {Number: 21, State: PROpen},
	}
	if got := MergePRs(PRSet{ByBranch: open}, PRSet{ByBranch: all}).ByBranch; !reflect.DeepEqual(got, want) {
		t.Errorf("MergePRs(PRSet{ByBranch: open}, PRSet{ByBranch: all}).ByBranch = %v, want %v", got, want)
	}
	if got := MergePRs(PRSet{ByBranch: all}, PRSet{ByBranch: open}).ByBranch; !reflect.DeepEqual(got, want) {
		t.Errorf("MergePRs(PRSet{ByBranch: all}, PRSet{ByBranch: open}).ByBranch = %v, want %v", got, want)
	}
	if got := MergePRs(PRSet{}, PRSet{ByBranch: map[string]PR{}}); !got.Loaded() || len(got.ByBranch) != 0 {
		t.Errorf("MergePRs(zero, empty) = %#v, want a loaded, empty set", got)
	}
}

const ghFixture = `[{"headRefName":"feat/eval-quant-tier-suite","headRepository":{"id":"R_kgDOUF9s9w","name":"local-ai-setup","nameWithOwner":"Ohanaverse/Local-AI-Setup"},"isCrossRepository":false,"isDraft":false,"number":404,"state":"MERGED","title":"feat(llmbench): eval-quant-tier suite"}]`

// TestParsePRsReadsTheHeadRepoAsRepo pins that a listing's Repo is its PRs'
// head repository, lower-cased: every kept PR is same-repo, so it is the
// base repo the picker later matches remotes against.
func TestParsePRsReadsTheHeadRepoAsRepo(t *testing.T) {
	got, err := parsePRs([]byte(ghFixture))
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "ohanaverse/local-ai-setup" {
		t.Errorf("Repo = %q, want ohanaverse/local-ai-setup", got.Repo)
	}
	if got.ByBranch["feat/eval-quant-tier-suite"].Number != 404 {
		t.Errorf("ByBranch = %+v, want #404", got.ByBranch)
	}
}

// TestParsePRsWithoutHeadRepositoryHasEmptyRepoButStillMapsBranches pins that
// a PR with no headRepository (a deleted fork) costs the Repo, not the tag.
func TestParsePRsWithoutHeadRepositoryHasEmptyRepoButStillMapsBranches(t *testing.T) {
	got, err := parsePRs([]byte(`[{"number": 5, "state": "OPEN", "headRefName": "x"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "" || got.ByBranch["x"].Number != 5 {
		t.Errorf("got %+v, want empty Repo and #5 under x", got)
	}
}

// TestMergePRsKeepsTheFirstNonEmptyRepo pins that the joined set carries
// whichever listing's Repo is non-empty, the first when both are.
func TestMergePRsKeepsTheFirstNonEmptyRepo(t *testing.T) {
	a, b := PRSet{Repo: "o/a"}, PRSet{Repo: "o/b"}
	if got := MergePRs(a, b).Repo; got != "o/a" {
		t.Errorf("MergePRs(a, b).Repo = %q, want o/a", got)
	}
	if got := MergePRs(PRSet{}, b).Repo; got != "o/b" {
		t.Errorf("MergePRs(empty, b).Repo = %q, want o/b", got)
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
