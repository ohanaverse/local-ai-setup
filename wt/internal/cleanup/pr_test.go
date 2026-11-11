package cleanup

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// stubGh replaces the gh seam for one test and restores it afterwards.
func stubGh(t *testing.T, fn func(dir, branch string) ([]byte, error)) {
	t.Helper()
	old := ghPRList
	ghPRList = fn
	t.Cleanup(func() { ghPRList = old })
}

func prJSON(number int, state, repo string) map[string]any {
	return map[string]any{
		"number": number, "state": state, "headRefOid": "abc", "baseRefName": "main",
		"mergeCommit":       map[string]any{"oid": "def"},
		"isCrossRepository": strings.ToLower(repo) != "o/r",
		"headRepository":    map[string]any{"name": "r", "nameWithOwner": repo},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestPRLookupFailureIsUnknown checks a failing gh call is UNKNOWN with a
// message, never "none": a false "no PR" would remove an open-PR veto.
func TestPRLookupFailureIsUnknown(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) { return nil, errors.New("boom") })
	r := LookupPRs("", []string{"topic"}, "o/r", 2, GatherInputs{})["topic"]
	if r.Lookup != LookupUnknown || r.Err == "" {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupBadJSONIsUnknown checks output that is not the expected JSON
// is UNKNOWN too.
func TestPRLookupBadJSONIsUnknown(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) { return []byte("not json"), nil })
	if r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]; r.Lookup != LookupUnknown {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupEmptyIsNone checks an answered, empty listing is "none".
func TestPRLookupEmptyIsNone(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]
	if r.Lookup != LookupNone || len(r.PRs) != 0 {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupKnownFields checks the fields of an own-repo PR are decoded.
func TestPRLookupKnownFields(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) {
		return mustJSON(t, []any{prJSON(7, "MERGED", "O/R")}), nil
	})
	r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]
	want := PRInfo{Number: 7, State: "MERGED", HeadRefOid: "abc", BaseRef: "main", MergeCommit: "def"}
	if r.Lookup != LookupKnown || len(r.PRs) != 1 || r.PRs[0] != want {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupFiltersForkPRs checks a PR from another head repository is
// dropped, leaving "none" when it was the only one.
func TestPRLookupFiltersForkPRs(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) {
		return mustJSON(t, []any{prJSON(1, "OPEN", "someone/r")}), nil
	})
	if r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]; r.Lookup != LookupNone {
		t.Fatalf("got %+v", r)
	}
	stubGh(t, func(string, string) ([]byte, error) {
		return mustJSON(t, []any{prJSON(1, "OPEN", "someone/r"), prJSON(2, "OPEN", "o/r")}), nil
	})
	r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]
	if r.Lookup != LookupKnown || len(r.PRs) != 1 || r.PRs[0].Number != 2 {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupHeadRepoFromOwnerAndName checks the head repo is derived from
// headRepositoryOwner.login + headRepository.name when nameWithOwner is absent.
func TestPRLookupHeadRepoFromOwnerAndName(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) {
		return []byte(`[{"number":3,"state":"OPEN","headRepository":{"name":"r"},"headRepositoryOwner":{"login":"O"}}]`), nil
	})
	if r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]; r.Lookup != LookupKnown {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupTruncatedIsUnknown checks a full page (the limit we ask for)
// may hide more PRs, so it is UNKNOWN even when every PR is a fork's.
func TestPRLookupTruncatedIsUnknown(t *testing.T) {
	var prs []any
	for i := 1; i <= prLimit; i++ {
		prs = append(prs, prJSON(i, "CLOSED", "someone/r"))
	}
	stubGh(t, func(string, string) ([]byte, error) { return mustJSON(t, prs), nil })
	r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]
	if r.Lookup != LookupUnknown || r.Err == "" {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupManyBranchesBounded checks every branch gets an answer and no
// more than parallel calls run at once.
func TestPRLookupManyBranchesBounded(t *testing.T) {
	var cur, peak atomic.Int32
	stubGh(t, func(string, string) ([]byte, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		cur.Add(-1)
		return []byte("[]"), nil
	})
	var bs []string
	for i := 0; i < 20; i++ {
		bs = append(bs, fmt.Sprintf("topic-%d", i))
	}
	got := LookupPRs("", bs, "o/r", 3, GatherInputs{})
	if len(got) != 20 || peak.Load() > 3 {
		t.Fatalf("len=%d peak=%d", len(got), peak.Load())
	}
	// parallel < 1 behaves as 1.
	if got := LookupPRs("", bs[:2], "o/r", 0, GatherInputs{}); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

// TestRealGhPRListRefusesInTestBinary checks the real call never reaches
// GitHub from a test binary.
func TestRealGhPRListRefusesInTestBinary(t *testing.T) {
	realGhPRListRefuses(t)
}

// TestPRResultZeroIsUnknown checks a zero PRResult (a map miss) is UNKNOWN,
// not Known or None, so it can never lift an open-PR veto.
func TestPRResultZeroIsUnknown(t *testing.T) {
	if (PRResult{}).Lookup != LookupUnknown {
		t.Fatalf("zero Lookup = %v", PRResult{}.Lookup)
	}
}

// TestPRLookupRenamedRepoSameRepoPRIsKnown checks a same-repository PR
// (isCrossRepository false) whose nameWithOwner differs from ownRepo, as after
// a rename or transfer, is kept: dropping it would lose an open-PR veto.
func TestPRLookupRenamedRepoSameRepoPRIsKnown(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) {
		row := prJSON(5, "OPEN", "new/name")
		row["isCrossRepository"] = false
		return mustJSON(t, []any{row}), nil
	})
	r := LookupPRs("", []string{"topic"}, "old/name", 1, GatherInputs{})["topic"]
	if r.Lookup != LookupKnown || len(r.PRs) != 1 {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupCrossRepoOtherRepoDropped checks a genuinely cross-repository
// PR from another repo is still dropped.
func TestPRLookupCrossRepoOtherRepoDropped(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) {
		row := prJSON(5, "OPEN", "someone/r")
		row["isCrossRepository"] = true
		return mustJSON(t, []any{row}), nil
	})
	if r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]; r.Lookup != LookupNone {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupNullHeadRepository checks a null headRepository keeps a
// same-repository PR and drops a cross-repository (deleted fork) one.
func TestPRLookupNullHeadRepository(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) {
		return []byte(`[{"number":1,"state":"OPEN","headRepository":null,"isCrossRepository":false}]`), nil
	})
	if r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]; r.Lookup != LookupKnown {
		t.Fatalf("same-repo got %+v", r)
	}
	stubGh(t, func(string, string) ([]byte, error) {
		return []byte(`[{"number":1,"state":"OPEN","headRepository":null,"isCrossRepository":true}]`), nil
	})
	if r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]; r.Lookup != LookupNone {
		t.Fatalf("deleted fork got %+v", r)
	}
}

// TestPRLookupEmptyOwnRepoIsUnknown checks an empty ownRepo makes every
// branch UNKNOWN with a message and never calls gh.
func TestPRLookupEmptyOwnRepoIsUnknown(t *testing.T) {
	var calls atomic.Int32
	stubGh(t, func(string, string) ([]byte, error) { calls.Add(1); return []byte("[]"), nil })
	got := LookupPRs("", []string{"a", "b"}, "  ", 2, GatherInputs{})
	if calls.Load() != 0 || len(got) != 2 {
		t.Fatalf("calls=%d got=%v", calls.Load(), got)
	}
	for b, r := range got {
		if r.Lookup != LookupUnknown || r.Err == "" {
			t.Fatalf("%s: %+v", b, r)
		}
	}
}

// TestPRLookupSchemaDriftIsUnknown checks an unrecognised state, a missing
// number, or output of `null` is UNKNOWN, so a gh schema change cannot
// quietly drop the veto.
func TestPRLookupSchemaDriftIsUnknown(t *testing.T) {
	for name, body := range map[string]string{
		"state":  `[{"number":1,"state":"WEIRD","isCrossRepository":false}]`,
		"number": `[{"number":0,"state":"OPEN","isCrossRepository":false}]`,
		"null":   `null`,
	} {
		stubGh(t, func(string, string) ([]byte, error) { return []byte(body), nil })
		r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]
		if r.Lookup != LookupUnknown || r.Err == "" {
			t.Fatalf("%s: %+v", name, r)
		}
	}
}

// ghLoginSpeech is what an unauthenticated gh prints on stderr: several
// lines, the second starting at column 0.
const ghLoginSpeech = "To get started with GitHub CLI, please run:  gh auth login\n" +
	"Alternatively, populate the GH_TOKEN environment variable with a GitHub API authentication token.\n"

// TestGhFailureIsOneLine checks that gh's multi-line stderr becomes a
// one-line error: its first line, whitespace collapsed, capped. Why: the
// error is every affected row's detail; a raw paste put gh's login speech
// under every row, its continuation lines at column 0 in --plain.
func TestGhFailureIsOneLine(t *testing.T) {
	got := ghFailure(errors.New("exit status 4"), []byte("\n  "+ghLoginSpeech)).Error()
	want := "gh pr list: exit status 4: To get started with GitHub CLI, please run: gh auth login"
	if got != want {
		t.Fatalf("ghFailure = %q, want %q", got, want)
	}
	long := ghFailure(errors.New("exit status 1"), []byte(strings.Repeat("word ", 100))).Error()
	if n := len([]rune(long)); n > ghErrMax || !strings.HasSuffix(long, "…") {
		t.Fatalf("long error: %d runes %q, want at most %d ending in …", n, long, ghErrMax)
	}
}

// TestPRLookupErrorIsOneLine checks that a lookup failure's Err is one line
// whatever error the gh call returned. Why: Err is a reason's detail in the
// list, the plain output and the JSON; a newline in it breaks the row.
func TestPRLookupErrorIsOneLine(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) { return nil, errors.New(ghLoginSpeech) })
	r := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]
	if r.Lookup != LookupUnknown || strings.ContainsAny(r.Err, "\r\n") || !strings.HasPrefix(r.Err, "To get started") {
		t.Fatalf("got %+v, want Unknown with a one-line Err", r)
	}
}

func realGhPRListRefuses(t *testing.T) {
	if _, err := realGhPRList("", "topic"); !errors.Is(err, errGhUnderTest) {
		t.Fatalf("err = %v", err)
	}
}

// TestLookupBaseOfIsTriState drives the open-PRs-by-base query through its
// answers. What: a list is known, an empty list is none, and an error, null,
// bad JSON or a full page (possible truncation) is unknown. Why: the delete of
// an origin branch closes every PR that targets it, so only a complete answer
// may say "none" (fail closed, like the head lookup).
func TestLookupBaseOfIsTriState(t *testing.T) {
	full := make([]map[string]any, prLimit)
	for i := range full {
		full[i] = map[string]any{"number": i + 1}
	}
	cases := []struct {
		name string
		body []byte
		err  error
		want Lookup
	}{
		{"one", []byte(`[{"number":4,"headRefName":"x","isCrossRepository":true}]`), nil, LookupKnown},
		{"none", []byte(`[]`), nil, LookupNone},
		{"error", nil, errors.New("boom"), LookupUnknown},
		{"null", []byte(`null`), nil, LookupUnknown},
		{"bad json", []byte(`{`), nil, LookupUnknown},
		{"full page", mustJSON(t, full), nil, LookupUnknown},
		{"bad number", []byte(`[{"number":0}]`), nil, LookupUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubGhBase(t, func(string, string) ([]byte, error) { return c.body, c.err })
			got := LookupBaseOf("", []string{"topic"}, 1, GatherInputs{})["topic"]
			if got.Lookup != c.want || (c.want == LookupUnknown && got.Err == "") {
				t.Fatalf("got %+v, want lookup %v (with an error text when unknown)", got, c.want)
			}
			if c.want == LookupKnown && (got.PRs[0].Number != 4 || got.PRs[0].State != "OPEN" || got.PRs[0].BaseRef != "topic") {
				t.Fatalf("PR fact: %+v", got.PRs)
			}
		})
	}
}

// TestGatherAsksWhoTargetsEachMergeCandidate gathers a repo whose origin
// branch "topic" is merged and has a dependent PR that gh reports on the base
// query only (its head branch exists nowhere locally). What: the row is
// in_use with base_of_open_pr, and the head-lookup alone would have missed it.
// Why: this is the real blind spot of the stacked-PR check; deleting topic
// would close that PR.
func TestGatherAsksWhoTargetsEachMergeCandidate(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Push("topic")
	tip := f.git("rev-parse", "main")
	mergedPRStub(t, "topic", 8, tip, tip)
	var asked atomic.Int32
	stubGhBase(t, func(_, base string) ([]byte, error) {
		asked.Add(1)
		if base == "topic" {
			return []byte(`[{"number":31,"headRefName":"child","isCrossRepository":true}]`), nil
		}
		return []byte(`[]`), nil
	})
	s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Fetch: true, OriginRows: true})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, Classify(s, Options{Now: later()}), KindRemote, "topic")
	if row.Status != StatusInUse || !slices.Contains(reasonCodes(row.Blockers), BlockBaseOfOpenPR) {
		t.Fatalf("row %s blockers %v, want in_use with %s", row.Status, reasonCodes(row.Blockers), BlockBaseOfOpenPR)
	}
	if asked.Load() == 0 {
		t.Fatal("the base query was never run")
	}
}

// TestGatherBaseQueryFailureMakesTheRowUnsure fails the base query while the
// fetch is fine. What: a merged origin row becomes unknown/cannot_verify.
// Why: an answered "fetch ok" does not prove that no PR targets the branch.
func TestGatherBaseQueryFailureMakesTheRowUnsure(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Push("topic")
	tip := f.git("rev-parse", "main")
	mergedPRStub(t, "topic", 8, tip, tip)
	stubGhBase(t, func(string, string) ([]byte, error) { return nil, errors.New("gh down") })
	s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Fetch: true, OriginRows: true})
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(t, Classify(s, Options{Now: later()}), KindRemote, "topic")
	if row.Status == StatusProven || !row.Unknown || !slices.Contains(reasonCodes(row.Reasons), ReasonCannotVerify) {
		t.Fatalf("row %s unknown=%v %v", row.Status, row.Unknown, reasonCodes(row.Reasons))
	}
}

// TestPRHooksArePerCall proves the gh pr-list hooks are per-call state on
// GatherInputs, not the swapped package vars: two concurrent lookups, each
// carrying its own PRList and PRBaseList hooks, are served only by their own
// hook and land on their own answer. Why: Ruling 1 — parallel test suites
// share one process, so a package-var seam lets one test's stub leak into
// another test's run (or be overwritten by it).
func TestPRHooksArePerCall(t *testing.T) {
	tags := [2]string{"a", "b"}
	var head, base [2]atomic.Int64
	var ins [2]GatherInputs
	for i, tag := range tags {
		ins[i] = GatherInputs{
			PRList: func(_, branch string) ([]byte, error) {
				head[i].Add(1)
				if branch != tag+"-head" {
					t.Errorf("PRList hook of %q called for branch %q", tag, branch)
				}
				return []byte("[]"), nil
			},
			PRBaseList: func(_, name string) ([]byte, error) {
				base[i].Add(1)
				if name != tag+"-base" {
					t.Errorf("PRBaseList hook of %q called for base %q", tag, name)
				}
				return []byte("[]"), nil
			},
		}
	}
	var heads, bases [2]map[string]PRResult
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range ins {
		go func(i int) {
			defer wg.Done()
			heads[i] = LookupPRs("", []string{tags[i] + "-head"}, "o/r", 1, ins[i])
			bases[i] = LookupBaseOf("", []string{tags[i] + "-base"}, 1, ins[i])
		}(i)
	}
	wg.Wait()
	for i, tag := range tags {
		if head[i].Load() < 1 || base[i].Load() < 1 {
			t.Errorf("hooks of %q: %d head and %d base calls, want at least one each", tag, head[i].Load(), base[i].Load())
		}
		// Each run lands on its own hook's clean answer, for its own name and
		// nothing else: the outputs carry no cross-talk between the two runs.
		if r := heads[i][tags[i]+"-head"]; len(heads[i]) != 1 || r.Lookup != LookupNone || r.Err != "" {
			t.Errorf("head results of %q: %+v", tag, heads[i])
		}
		if r := bases[i][tags[i]+"-base"]; len(bases[i]) != 1 || r.Lookup != LookupNone || r.Err != "" {
			t.Errorf("base results of %q: %+v", tag, bases[i])
		}
	}
}

// TestPRHookFallbackStillPoisoned checks a zero GatherInputs resolves to the
// legacy seam, whose real call refuses in a test binary (errGhUnderTest): a
// test that forgets both the stub and the hooks errors instead of asking the
// developer's GitHub account. Why: the forgetting-to-stub guarantee must
// survive the per-call hooks (Ruling 1).
func TestPRHookFallbackStillPoisoned(t *testing.T) {
	// Put the seams back at their production default (TestMain replaces them
	// with its own poison): the real call is what an unstubbed, unhooked
	// lookup gets.
	t.Cleanup(StubPRListForTest(realGhPRList))
	t.Cleanup(StubPRBaseListForTest(realGhPRBaseList))
	if _, err := prListFn(GatherInputs{})("", "topic"); !errors.Is(err, errGhUnderTest) {
		t.Fatalf("prListFn(zero Inputs) err = %v, want %v", err, errGhUnderTest)
	}
	if _, err := prBaseListFn(GatherInputs{})("", "topic"); !errors.Is(err, errGhUnderTest) {
		t.Fatalf("prBaseListFn(zero Inputs) err = %v, want %v", err, errGhUnderTest)
	}
}

// failGh makes every gh call fail, as with no gh or no network.
func failGh(t *testing.T) {
	t.Helper()
	stubGh(t, func(string, string) ([]byte, error) { return nil, errors.New("gh: offline") })
	stubGhBase(t, func(string, string) ([]byte, error) { return nil, errors.New("gh: offline") })
}

func countingGh(t *testing.T) *int32 {
	t.Helper()
	var n int32
	stubGh(t, func(string, string) ([]byte, error) { atomic.AddInt32(&n, 1); return []byte("[]"), nil })
	stubGhBase(t, func(string, string) ([]byte, error) { atomic.AddInt32(&n, 1); return []byte("[]"), nil })
	return &n
}

func gatherIn(f *fixture) GatherInputs {
	return GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Parallel: 2, OriginRows: true}
}

// TestGatherSkipPRsMakesNoGhCall: the list must render from git state alone.
func TestGatherSkipPRsMakesNoGhCall(t *testing.T) {
	f := newFixture(t)
	f.Branch("done")
	f.Push("done")
	n := countingGh(t)
	in := gatherIn(f)
	in.SkipPRs = true
	s, err := Gather(Git{Dir: f.Dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	if *n != 0 {
		t.Fatalf("Gather(SkipPRs) made %d gh calls", *n)
	}
	if s.PRsAsked {
		t.Fatal("PRsAsked is true after a skipped Gather")
	}
}

// TestFillPRsMatchesAFullGather: skip then fill equals one full Gather.
func TestFillPRsMatchesAFullGather(t *testing.T) {
	f := newFixture(t)
	f.Branch("done")
	f.Push("done")
	f.Worktree("topic")
	noGh(t)
	full, err := Gather(Git{Dir: f.Dir}, gatherIn(f))
	if err != nil {
		t.Fatal(err)
	}
	in := gatherIn(f)
	in.SkipPRs = true
	s, err := Gather(Git{Dir: f.Dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	FillPRs(&s, in)
	if !s.PRsAsked || s.PRsSkipped {
		t.Fatalf("after FillPRs: PRsAsked %v PRsSkipped %v", s.PRsAsked, s.PRsSkipped)
	}
	if !reflect.DeepEqual(s, full) {
		t.Fatalf("filled snapshot differs from a full Gather:\n got  %+v\n want %+v", s, full)
	}
}

// TestPRUnknownIsNeverProvenWhenAsked: git proves the content, gh is down:
// less_sure with pr_unknown, tagged "PR ?" (Review Focus 2).
func TestPRUnknownIsNeverProvenWhenAsked(t *testing.T) {
	f := newFixture(t)
	f.Branch("done")
	failGh(t)
	s := gather(t, f)
	if !s.PRsAsked {
		t.Fatal("a full Gather must set PRsAsked")
	}
	row := findRow(t, Classify(s, Options{Now: later()}), KindBranch, "done")
	if row.Evidence == "" {
		t.Fatalf("fixture has no git proof: %+v", row)
	}
	if row.Status == StatusProven || !row.PRUnknown || !hasReason(row, ReasonPRUnknown) {
		t.Fatalf("row = %+v, want not proven, PRUnknown, pr_unknown", row)
	}
	if got := RowTags(row); len(got) != 1 || got[0] != "PR ?" {
		t.Fatalf("tags %v", got)
	}
}

func hasReason(r Row, code string) bool {
	return slices.ContainsFunc(r.Reasons, func(x Reason) bool { return x.Code == code })
}

// TestPRUnknownAppliesToWorktreeBranchAndOriginRows covers a skipped
// snapshot (nothing asked) and a failed one.
func TestPRUnknownAppliesToWorktreeBranchAndOriginRows(t *testing.T) {
	for _, mode := range []string{"skipped", "failed"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.Branch("bdone")
			f.Branch("wdone")
			f.git("worktree", "add", filepath.Join(filepath.Dir(f.Dir), "wdone"), "wdone")
			f.Branch("odone")
			f.Push("odone")
			in := gatherIn(f)
			if mode == "skipped" {
				in.SkipPRs = true
				stubGh(t, func(string, string) ([]byte, error) { t.Error("gh called"); return nil, errors.New("x") })
			} else {
				failGh(t)
			}
			s, err := Gather(Git{Dir: f.Dir}, in)
			if err != nil {
				t.Fatal(err)
			}
			rep := Classify(s, Options{Now: later()})
			for _, row := range []Row{
				findRow(t, rep, KindBranch, "bdone"),
				findRow(t, rep, KindWorktree, "wdone"),
				findRow(t, rep, KindRemote, "odone"),
			} {
				if row.Status == StatusProven || !row.PRUnknown {
					t.Errorf("%s %s: status %s PRUnknown %v", row.Kind, row.Name, row.Status, row.PRUnknown)
				}
			}
			if mode == "skipped" {
				for _, row := range rep.Rows {
					if row.Status == StatusProven {
						t.Errorf("not-yet-filled snapshot produced proven row %s %s", row.Kind, row.Name)
					}
				}
			}
		})
	}
}

// TestPRTagOnceKnown: after a fill the proven row has no PR ? and the PR tag shows.
func TestPRTagOnceKnown(t *testing.T) {
	f := newFixture(t)
	f.Branch("done")
	noGh(t)
	s := gather(t, f)
	row := findRow(t, Classify(s, Options{Now: later()}), KindBranch, "done")
	if row.PRUnknown || row.Status != StatusProven {
		t.Fatalf("filled row %+v", row)
	}
}
