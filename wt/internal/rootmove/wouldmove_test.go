package rootmove

import (
	"encoding/json"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// TestWouldMove builds the would_move outcome from a Plan literal with no
// repository behind it and checks the status, that the plan (warnings and
// counts included) is carried, that nothing is marked done (Created false,
// Completed empty but non-nil, no record, no fast-forward), and that the
// encoded document says outcome would_move. Why: --dry-run prints exactly
// this, and it must run no git. (Schema validity of the encoded form is held
// by cmd/wt's TestRestoreRootWouldMoveEncodesSchemaValid.)
func TestWouldMove(t *testing.T) {
	p := Plan{Repo: "/r", Branch: "feat", Default: "main", Head: "abc", DestPath: "/r/.worktrees/feat", DefaultFF: true,
		Counts: rescue.Counts{Staged: 2}, Warnings: []string{"w1"}}
	o := WouldMove(&p)
	if o.Status != rescue.StatusWouldMove || o.Code != "" || o.Reason != "" {
		t.Errorf("status %q code %q reason %q", o.Status, o.Code, o.Reason)
	}
	if o.Plan.Branch != "feat" || o.Plan.Counts.Staged != 2 || len(o.Plan.Warnings) != 1 || !o.Plan.DefaultFF {
		t.Errorf("plan not carried: %+v", o.Plan)
	}
	if o.Created || o.Record != nil || o.FastForwarded || o.Completed == nil || len(o.Completed) != 0 {
		t.Errorf("created %v record %v ff %v completed %#v", o.Created, o.Record, o.FastForwarded, o.Completed)
	}
	p.Branch = "changed"
	if o.Plan.Branch != "feat" {
		t.Error("the outcome shares the caller's plan")
	}
	b, err := EncodeResult(o, Meta{Repo: "/r", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v["outcome"] != "would_move" || v["dry_run"] != true || v["created"] != false {
		t.Errorf("document: %s", b)
	}
}
