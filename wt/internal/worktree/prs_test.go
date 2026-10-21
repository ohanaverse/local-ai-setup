package worktree

import (
	"errors"
	"reflect"
	"testing"
)

// head is the key a PR from repo's branch is kept under.
func head(repo, branch string) PRKey { return PRKey{Repo: repo, Branch: branch} }

// TestParsePRsMapsEachHeadToItsLatestPR pins the JSON-to-tag mapping the
// picker shows: one PR per head (repository, branch), an open (or draft) PR
// winning over a closed or merged one and the highest number winning
// otherwise, an open draft reading "draft", merged and closed reading as
// themselves, and a fork's PR kept under its own head repository, apart from
// the base repository's branch of the same name (#387). A regression shows a
// branch's old closed PR instead of its open one — even when the closed one
// is newer — or files a fork's PR under the base repository's branch, where
// it would tag the base remote's row.
func TestParsePRsMapsEachHeadToItsLatestPR(t *testing.T) {
	data := []byte(`[
		{"number": 383, "state": "OPEN", "isDraft": false, "headRefName": "fix/stop", "isCrossRepository": false, "title": "Stop waits", "headRepository": {"nameWithOwner": "o/base"}},
		{"number": 380, "state": "CLOSED", "isDraft": false, "headRefName": "fix/stop", "isCrossRepository": false, "title": "Old attempt", "headRepository": {"nameWithOwner": "o/base"}},
		{"number": 390, "state": "OPEN", "isDraft": false, "headRefName": "fix/stop", "isCrossRepository": true, "title": "Fork PR", "headRepository": {"nameWithOwner": "Me/Fork"}},
		{"number": 382, "state": "MERGED", "isDraft": false, "headRefName": "fix/sync", "isCrossRepository": false, "title": "Sync waits", "headRepository": {"nameWithOwner": "o/base"}},
		{"number": 381, "state": "OPEN", "isDraft": true, "headRefName": "feat/draft", "isCrossRepository": false, "title": "WIP", "headRepository": {"nameWithOwner": "o/base"}},
		{"number": 379, "state": "CLOSED", "isDraft": false, "headRefName": "feat/dropped", "isCrossRepository": false, "title": "Dropped", "headRepository": {"nameWithOwner": "o/base"}},
		{"number": 378, "state": "OPEN", "isDraft": false, "headRefName": "only/fork", "isCrossRepository": true, "title": "Fork only", "headRepository": {"nameWithOwner": "me/fork"}},
		{"number": 300, "state": "CLOSED", "isDraft": false, "headRefName": "fix/dup", "isCrossRepository": false, "title": "Duplicate", "headRepository": {"nameWithOwner": "o/base"}},
		{"number": 50, "state": "OPEN", "isDraft": true, "headRefName": "fix/dup", "isCrossRepository": false, "title": "Live", "headRepository": {"nameWithOwner": "o/base"}},
		{"number": 0, "state": "OPEN", "headRefName": "no/number", "headRepository": {"nameWithOwner": "o/base"}},
		{"number": 377, "state": "OPEN", "headRefName": "", "headRepository": {"nameWithOwner": "o/base"}}
	]`)
	got, err := parsePRs(data)
	if err != nil {
		t.Fatalf("parsePRs: %v", err)
	}
	want := map[PRKey]PR{
		head("o/base", "fix/stop"):     {Number: 383, State: PROpen, Title: "Stop waits"},
		head("me/fork", "fix/stop"):    {Number: 390, State: PROpen, Title: "Fork PR"},
		head("o/base", "fix/sync"):     {Number: 382, State: PRMerged, Title: "Sync waits"},
		head("o/base", "feat/draft"):   {Number: 381, State: PRDraft, Title: "WIP"},
		head("o/base", "feat/dropped"): {Number: 379, State: PRClosed, Title: "Dropped"},
		head("me/fork", "only/fork"):   {Number: 378, State: PROpen, Title: "Fork only"},
		head("o/base", "fix/dup"):      {Number: 50, State: PRDraft, Title: "Live"},
	}
	if !reflect.DeepEqual(got.ByHead, want) {
		t.Errorf("parsePRs =\n%#v\nwant\n%#v", got.ByHead, want)
	}
}

// TestParsePRsDropsAForkPRWithNoHeadRepository pins the deleted-fork case: a
// cross-repository PR whose head repository gh reports as null (the fork was
// deleted) names a branch in no repository a remote can point at, so it is
// dropped, never filed under an empty repository where a local branch of the
// same name would pick it up. A same-repository PR with no head repository
// (a gh that does not report the field) is still kept, under an empty
// repository: only a local or worktree row can find it, never a remote row.
func TestParsePRsDropsAForkPRWithNoHeadRepository(t *testing.T) {
	got, err := parsePRs([]byte(`[
		{"number": 9, "state": "OPEN", "headRefName": "gone", "isCrossRepository": true, "headRepository": null},
		{"number": 8, "state": "OPEN", "headRefName": "gone2", "isCrossRepository": true},
		{"number": 5, "state": "OPEN", "headRefName": "x", "isCrossRepository": false}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[PRKey]PR{head("", "x"): {Number: 5, State: PROpen}}
	if !reflect.DeepEqual(got.ByHead, want) {
		t.Errorf("ByHead = %#v, want %#v", got.ByHead, want)
	}
	if _, ok := got.ForRemote("", "x"); ok {
		t.Error("ForRemote found a PR under an empty repository")
	}
	if pr, ok := got.ForBranch("x"); !ok || pr.Number != 5 {
		t.Errorf("ForBranch(x) = %+v, %v, want #5", pr, ok)
	}
}

// TestParsePRsKeepsTheHighestNumberWhateverTheOrder pins that the choice
// between a head's PRs does not depend on the order gh lists them in.
// Without it a re-ordered listing would flip a branch's tag between runs.
func TestParsePRsKeepsTheHighestNumberWhateverTheOrder(t *testing.T) {
	got, err := parsePRs([]byte(`[
		{"number": 10, "state": "CLOSED", "headRefName": "b", "headRepository": {"nameWithOwner": "o/r"}},
		{"number": 12, "state": "OPEN", "headRefName": "b", "headRepository": {"nameWithOwner": "o/r"}},
		{"number": 11, "state": "MERGED", "headRefName": "b", "headRepository": {"nameWithOwner": "o/r"}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if pr := got.ByHead[head("o/r", "b")]; pr.Number != 12 || pr.State != PROpen {
		t.Errorf("b = %+v, want #12 open", pr)
	}
}

// TestPRsFailureIsAnEmptyLoadedSet pins the silent fallback: gh missing,
// failing, or printing something that is not the expected JSON leaves the
// picker with no tags and no error to show. A regression would surface a gh error (or
// panic on a nil map) in a picker that worked fine before PR tags existed.
func TestPRsFailureIsAnEmptyLoadedSet(t *testing.T) {
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
			if !got.Loaded() || len(got.ByHead) != 0 {
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
		return []byte(`[{"number": 5, "state": "OPEN", "headRefName": "x", "headRepository": {"nameWithOwner": "o/r"}}]`), nil
	}))
	got := PRs("/repo", PRListOpen)
	if want := []string{"/repo open"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if pr := got.ByHead[head("o/r", "x")]; pr.Number != 5 {
		t.Errorf("x = %+v, want #5", pr)
	}
}

// TestMergePRsPrefersOpenThenTheHigherNumberInEitherOrder pins how the
// picker joins its two listings: every head of both, and a head in both
// keeps its open PR over a closed or merged one, else the higher-numbered
// PR, whichever listing came first — the same rule as within one listing.
// The same branch name in two repositories is two heads and never merged
// into one (#387). A regression lets the slower listing's older PR overwrite
// a newer one, lets a newer closed duplicate hide the branch's open PR,
// drops an old open PR only the open listing had, or lets a fork's PR
// replace the base repository's PR for a branch of the same name.
func TestMergePRsPrefersOpenThenTheHigherNumberInEitherOrder(t *testing.T) {
	open := map[PRKey]PR{
		head("o/r", "old"):  {Number: 7, State: PROpen},
		head("o/r", "b"):    {Number: 10, State: PROpen},
		head("o/r", "c"):    {Number: 20, State: PRDraft},
		head("me/f", "c"):   {Number: 30, State: PROpen},
		head("me/f", "dup"): {Number: 31, State: PROpen},
	}
	all := map[PRKey]PR{
		head("o/r", "new"): {Number: 900, State: PRMerged},
		head("o/r", "b"):   {Number: 12, State: PRClosed},
		head("o/r", "c"):   {Number: 21, State: PROpen},
		head("o/r", "dup"): {Number: 40, State: PRMerged},
	}
	want := map[PRKey]PR{
		head("o/r", "old"):  {Number: 7, State: PROpen},
		head("o/r", "new"):  {Number: 900, State: PRMerged},
		head("o/r", "b"):    {Number: 10, State: PROpen},
		head("o/r", "c"):    {Number: 21, State: PROpen},
		head("me/f", "c"):   {Number: 30, State: PROpen},
		head("me/f", "dup"): {Number: 31, State: PROpen},
		head("o/r", "dup"):  {Number: 40, State: PRMerged},
	}
	if got := MergePRs(PRSet{ByHead: open}, PRSet{ByHead: all}).ByHead; !reflect.DeepEqual(got, want) {
		t.Errorf("MergePRs(open, all).ByHead = %v, want %v", got, want)
	}
	if got := MergePRs(PRSet{ByHead: all}, PRSet{ByHead: open}).ByHead; !reflect.DeepEqual(got, want) {
		t.Errorf("MergePRs(all, open).ByHead = %v, want %v", got, want)
	}
	if got := MergePRs(PRSet{}, PRSet{ByHead: map[PRKey]PR{}}); !got.Loaded() || len(got.ByHead) != 0 {
		t.Errorf("MergePRs(zero, empty) = %#v, want a loaded, empty set", got)
	}
}

// ghFixture is gh-shaped `gh pr list --json` output, with a deliberately
// mixed-case owner/repo so the lower-casing of the head repository is
// exercised. The fork PR comes first, so BaseRepo cannot be read off it.
const ghFixture = `[{"headRefName":"feat/x","headRepository":{"id":"R_x","name":"Fork","nameWithOwner":"Keith/Fork"},"isCrossRepository":true,"isDraft":false,"number":405,"state":"OPEN","title":"fork work"},{"headRefName":"feat/eval-quant-tier-suite","headRepository":{"id":"R_kgDOUF9s9w","name":"local-ai-setup","nameWithOwner":"Ohanaverse/Local-AI-Setup"},"isCrossRepository":false,"isDraft":false,"number":404,"state":"MERGED","title":"feat(llmbench): eval-quant-tier suite"}]`

// TestParsePRsKeysByTheLowerCasedHeadRepoAndReadsTheBaseRepo pins the two
// repository readings of a listing: every PR is keyed by its head
// repository, lower-cased (GitHub names are case-insensitive, and remotes
// are compared lower-cased), and BaseRepo is the head repository of a
// same-repository PR — the repository gh listed — never a fork's. A
// regression misses a remote spelled differently from gh's answer, or names
// a fork in the picker's "no remote is …" notice.
func TestParsePRsKeysByTheLowerCasedHeadRepoAndReadsTheBaseRepo(t *testing.T) {
	got, err := parsePRs([]byte(ghFixture))
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseRepo != "ohanaverse/local-ai-setup" {
		t.Errorf("BaseRepo = %q, want ohanaverse/local-ai-setup", got.BaseRepo)
	}
	if pr := got.ByHead[head("ohanaverse/local-ai-setup", "feat/eval-quant-tier-suite")]; pr.Number != 404 {
		t.Errorf("ByHead = %+v, want #404 under ohanaverse/local-ai-setup", got.ByHead)
	}
	if pr := got.ByHead[head("keith/fork", "feat/x")]; pr.Number != 405 {
		t.Errorf("ByHead = %+v, want #405 under keith/fork", got.ByHead)
	}
}

// TestMergePRsKeepsTheFirstNonEmptyBaseRepo pins that the joined set carries
// whichever listing's BaseRepo is non-empty, the first when both are.
func TestMergePRsKeepsTheFirstNonEmptyBaseRepo(t *testing.T) {
	a, b := PRSet{BaseRepo: "o/a"}, PRSet{BaseRepo: "o/b"}
	if got := MergePRs(a, b).BaseRepo; got != "o/a" {
		t.Errorf("MergePRs(a, b).BaseRepo = %q, want o/a", got)
	}
	if got := MergePRs(PRSet{}, b).BaseRepo; got != "o/b" {
		t.Errorf("MergePRs(empty, b).BaseRepo = %q, want o/b", got)
	}
}

// TestPRSetForRemoteLooksUpItsOwnRepository pins the remote row's lookup
// (#387): a remote row finds the PR whose head is its own repository and
// branch, compared without case, and nothing else — not the same branch in
// another repository, and nothing at all for a remote whose repository is
// unknown or a row with no branch. A regression tags a fork's row with the
// base repository's PR (#388) or leaves your fork's rows untagged (#387).
func TestPRSetForRemoteLooksUpItsOwnRepository(t *testing.T) {
	s := PRSet{ByHead: map[PRKey]PR{
		head("o/base", "feat-x"):  {Number: 1, State: PROpen},
		head("me/fork", "feat-x"): {Number: 2, State: PROpen},
		head("", "feat-y"):        {Number: 3, State: PROpen},
	}}
	cases := []struct {
		repo, branch string
		want         int // 0: none
	}{
		{"o/base", "feat-x", 1},
		{"Me/Fork", "feat-x", 2},
		{"other/repo", "feat-x", 0},
		{"", "feat-x", 0},
		{"", "feat-y", 0},
		{"o/base", "", 0},
	}
	for _, c := range cases {
		pr, ok := s.ForRemote(c.repo, c.branch)
		if got := map[bool]int{true: pr.Number}[ok]; got != c.want {
			t.Errorf("ForRemote(%q, %q) = #%d (%v), want #%d", c.repo, c.branch, pr.Number, ok, c.want)
		}
	}
}

// TestPRSetForBranchPicksOneAcrossRepositories pins the local row's lookup
// (#387): a local branch or worktree has no repository, so its name is
// looked up under every head repository and one PR is chosen by the same
// rule as within a head — a live PR beats a closed or merged one, then the
// higher number. A regression shows a fork's merged PR over another
// repository's open one for the same branch name, or a tag that flips
// between picker opens with Go's map order.
func TestPRSetForBranchPicksOneAcrossRepositories(t *testing.T) {
	s := PRSet{ByHead: map[PRKey]PR{
		head("o/base", "x"):  {Number: 90, State: PRMerged},
		head("me/fork", "x"): {Number: 12, State: PROpen},
		head("o/base", "y"):  {Number: 20, State: PROpen},
		head("me/fork", "y"): {Number: 21, State: PRDraft},
		head("me/fork", "z"): {Number: 5, State: PRClosed},
	}}
	for branch, want := range map[string]int{"x": 12, "y": 21, "z": 5, "none": 0} {
		for i := 0; i < 20; i++ { // map order varies between iterations
			pr, ok := s.ForBranch(branch)
			if got := map[bool]int{true: pr.Number}[ok]; got != want {
				t.Fatalf("ForBranch(%q) = #%d (%v), want #%d", branch, pr.Number, ok, want)
			}
		}
	}
	if _, ok := s.ForBranch(""); ok {
		t.Error("ForBranch(\"\") found a PR")
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
