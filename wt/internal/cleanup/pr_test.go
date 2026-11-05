package cleanup

import (
	"encoding/json"
	"errors"
	"fmt"
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
		"mergeCommit":    map[string]any{"oid": "def"},
		"headRepository": map[string]any{"name": "r", "nameWithOwner": repo},
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
	r := LookupPRs("", []string{"topic"}, "o/r", 2)["topic"]
	if r.Lookup != LookupUnknown || r.Err == "" {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupBadJSONIsUnknown checks output that is not the expected JSON
// is UNKNOWN too.
func TestPRLookupBadJSONIsUnknown(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) { return []byte("not json"), nil })
	if r := LookupPRs("", []string{"topic"}, "o/r", 1)["topic"]; r.Lookup != LookupUnknown {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupEmptyIsNone checks an answered, empty listing is "none".
func TestPRLookupEmptyIsNone(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) { return []byte("[]"), nil })
	r := LookupPRs("", []string{"topic"}, "o/r", 1)["topic"]
	if r.Lookup != LookupNone || len(r.PRs) != 0 {
		t.Fatalf("got %+v", r)
	}
}

// TestPRLookupKnownFields checks the fields of an own-repo PR are decoded.
func TestPRLookupKnownFields(t *testing.T) {
	stubGh(t, func(string, string) ([]byte, error) {
		return mustJSON(t, []any{prJSON(7, "MERGED", "O/R")}), nil
	})
	r := LookupPRs("", []string{"topic"}, "o/r", 1)["topic"]
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
	if r := LookupPRs("", []string{"topic"}, "o/r", 1)["topic"]; r.Lookup != LookupNone {
		t.Fatalf("got %+v", r)
	}
	stubGh(t, func(string, string) ([]byte, error) {
		return mustJSON(t, []any{prJSON(1, "OPEN", "someone/r"), prJSON(2, "OPEN", "o/r")}), nil
	})
	r := LookupPRs("", []string{"topic"}, "o/r", 1)["topic"]
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
	if r := LookupPRs("", []string{"topic"}, "o/r", 1)["topic"]; r.Lookup != LookupKnown {
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
	r := LookupPRs("", []string{"topic"}, "o/r", 1)["topic"]
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
	got := LookupPRs("", bs, "o/r", 3)
	if len(got) != 20 || peak.Load() > 3 {
		t.Fatalf("len=%d peak=%d", len(got), peak.Load())
	}
	// parallel < 1 behaves as 1.
	if got := LookupPRs("", bs[:2], "o/r", 0); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

// TestRealGhPRListRefusesInTestBinary checks the real call never reaches
// GitHub from a test binary.
func TestRealGhPRListRefusesInTestBinary(t *testing.T) {
	if _, err := realGhPRList("", "topic"); !errors.Is(err, errGhUnderTest) {
		t.Fatalf("err = %v", err)
	}
}
