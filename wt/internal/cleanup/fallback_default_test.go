package cleanup

import (
	"slices"
	"testing"
)

// fallbackRepo is a repository with no remote: main has a commit
// 'only-on-main' no other ref holds, and the primary checkout is on topic,
// so main is a branch-only row. The base falls back to the local main.
func fallbackRepo(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.git("remote", "remove", "origin")
	f.git("switch", "-c", "topic")
	f.Checkout("main")
	f.Commit("only.txt", "only", "only-on-main")
	f.Checkout("topic")
	return f
}

// TestFallbackDefaultIsNotProvenAgainstItself: with no remote default the
// base is the local main, and main must not read as same-commit with itself
// and nothing to lose: it gets no merge evidence, and its commits (on no
// remote-tracking ref) are a loss.
func TestFallbackDefaultIsNotProvenAgainstItself(t *testing.T) {
	f := fallbackRepo(t)
	s := gather(t, f)
	if !s.Base.Fallback || s.Base.Ref != "main" {
		t.Fatalf("precondition: base %+v, want the local main", s.Base)
	}
	row := findRow(t, Classify(s, Options{Now: later()}), KindBranch, "main")
	if row.Evidence != "" {
		t.Errorf("evidence %q, want none: main was compared with itself", row.Evidence)
	}
	if row.Losses.Unpushed != 2 {
		t.Errorf("unpushed %d, want 2 (init and only-on-main are on no remote)", row.Losses.Unpushed)
	}
	if row.Status != StatusUniqueWork || row.BranchProven {
		t.Errorf("status %s proven %v, want unique_work, not proven", row.Status, row.BranchProven)
	}
	codes := []string{}
	for _, r := range row.Reasons {
		codes = append(codes, r.Code)
	}
	if !slices.Contains(codes, ReasonUnpushed) || !slices.Contains(codes, ReasonBaseIsLocal) {
		t.Errorf("reasons %v, want %s and %s", codes, ReasonUnpushed, ReasonBaseIsLocal)
	}
}

// TestFallbackDefaultPushedElsewhere: a local main whose commits are all on
// some remote-tracking ref (a remote that is not origin) has nothing unique,
// yet still no merge evidence: it reads less sure.
func TestFallbackDefaultPushedElsewhere(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "rename", "origin", "fork")
	f.git("switch", "-c", "topic")
	s := gather(t, f)
	if !s.Base.Fallback {
		t.Fatalf("precondition: base %+v, want fallback", s.Base)
	}
	row := findRow(t, Classify(s, Options{Now: later()}), KindBranch, "main")
	if row.Evidence != "" || row.Losses.Unpushed != 0 || row.Status != StatusLessSure || row.BranchProven {
		t.Errorf("row ev=%q unpushed=%d status=%s proven=%v, want no evidence, 0, less_sure, not proven",
			row.Evidence, row.Losses.Unpushed, row.Status, row.BranchProven)
	}
}
