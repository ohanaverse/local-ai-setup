package launchwarn

import (
	"reflect"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// TestFromRowMapsTheUnifiedRow checks FromRow's pure mapping of a unified-list
// row onto a launch Target: the PR state and number (lower-cased from GitHub's
// spelling), the raw dirt from the losses (a rescue record covering it does
// not make the changes disappear), the gone upstream, and the creation a row
// with no worktree owes — while the facts only a live read knows stay zero.
// It matters because these are the facts only the list gathered: a mapping
// error warns about the wrong thing, or a merged PR stops warning at all.
func TestFromRowMapsTheUnifiedRow(t *testing.T) {
	cases := []struct {
		name  string
		row   cleanup.Row
		want  Target
		codes []string // Collect's warning codes, in order
	}{
		{
			name: "merged PR, gone upstream, raw dirt",
			row: cleanup.Row{
				Kind:     cleanup.KindWorktree,
				Name:     "topic",
				Branch:   "topic",
				Path:     "/repo/.worktrees/topic",
				PRTag:    &cleanup.PRInfo{Number: 7, State: "MERGED"},
				Upstream: &cleanup.Upstream{Ref: "refs/remotes/origin/topic", Remote: "origin", Name: "topic", Gone: true},
				Losses:   cleanup.Losses{Dirty: true},
				// A rescue record that covers the dirt does not make the
				// changes disappear: Dirty is the raw losses', not
				// EffectiveLosses'.
				Preserved: &cleanup.Preserved{CoversDirt: true},
			},
			want: Target{
				Dir:          "/repo/.worktrees/topic",
				Branch:       "topic",
				Dirty:        true,
				UpstreamGone: true,
				PRState:      "merged",
				PRNumber:     7,
			},
			codes: []string{CodePRMerged, CodeUpstreamGone},
		},
		{
			name: "an unknown PR reads unknown and never warns a merge",
			row: cleanup.Row{
				Kind:      cleanup.KindWorktree,
				Name:      "topic",
				Branch:    "topic",
				Path:      "/repo/.worktrees/topic",
				PRTag:     &cleanup.PRInfo{Number: 7, State: "MERGED"}, // ignored while unknown
				PRUnknown: true,
			},
			want: Target{Dir: "/repo/.worktrees/topic", Branch: "topic", PRState: "unknown"},
		},
		{
			name: "no PR at all",
			row:  cleanup.Row{Kind: cleanup.KindWorktree, Name: "topic", Branch: "topic", Path: "/repo/.worktrees/topic"},
			want: Target{Dir: "/repo/.worktrees/topic", Branch: "topic"},
		},
		{
			name: "an open PR is carried, and only a merged one warns",
			row: cleanup.Row{
				Kind:   cleanup.KindWorktree,
				Name:   "topic",
				Branch: "topic",
				Path:   "/repo/.worktrees/topic",
				PRTag:  &cleanup.PRInfo{Number: 9, State: "OPEN"},
			},
			want: Target{Dir: "/repo/.worktrees/topic", Branch: "topic", PRState: "open", PRNumber: 9},
		},
		{
			name: "an upstream that is there is not gone",
			row: cleanup.Row{
				Kind:     cleanup.KindWorktree,
				Name:     "topic",
				Branch:   "topic",
				Path:     "/repo/.worktrees/topic",
				Upstream: &cleanup.Upstream{Ref: "refs/remotes/origin/topic", Remote: "origin", Name: "topic"},
			},
			want: Target{Dir: "/repo/.worktrees/topic", Branch: "topic"},
		},
		{
			name: "the root row is the root, and picking it is not consent",
			row: cleanup.Row{
				Kind:    cleanup.KindWorktree,
				Name:    "main",
				Branch:  "main",
				Path:    "/repo",
				Primary: true,
			},
			want:  Target{Dir: "/repo", Branch: "main", IsRoot: true},
			codes: []string{CodeRoot},
		},
		{
			name: "a remote-only row owes the tracking creation",
			row: cleanup.Row{
				Kind:   cleanup.KindRemote,
				Name:   "topic",
				Branch: "topic",
				Remote: "origin",
				Ref:    "refs/remotes/origin/topic",
				PRTag:  &cleanup.PRInfo{Number: 3, State: "MERGED"},
			},
			want: Target{
				Branch:   "topic",
				PRState:  "merged",
				PRNumber: 3,
				// The pick path's plan for "origin/topic" (ForNew over
				// worktree.Resolve) describes this same creation; only Path
				// is unknown to a row and stays for the plan to fill.
				Creation: &Creation{Branch: "topic", Tracks: "origin/topic", NewBranch: false, Path: ""},
			},
			codes: []string{CodePRMerged},
		},
		{
			name: "a bare branch row owes a worktree for the branch as it is",
			row:  cleanup.Row{Kind: cleanup.KindBranch, Name: "topic", Branch: "topic", Ref: "refs/heads/topic"},
			want: Target{
				Branch:   "topic",
				Creation: &Creation{Branch: "topic", Tracks: "", NewBranch: false, Path: ""},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FromRow(tc.row)
			if got == nil {
				t.Fatal("FromRow = nil")
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("target = %+v, want %+v", *got, tc.want)
			}
			warnings, _ := Collect(*got)
			gotCodes := make([]string, 0, len(warnings))
			for _, w := range warnings {
				gotCodes = append(gotCodes, w.Code)
			}
			if !reflect.DeepEqual(gotCodes, tc.codes) && !(len(gotCodes) == 0 && len(tc.codes) == 0) {
				t.Errorf("codes = %v, want %v", gotCodes, tc.codes)
			}
		})
	}
}

// TestFromRowCreationMirrorsThePickPlan checks the creation FromRow gives a
// row with no worktree is the one the pick path already plans for that row
// (worktree.Resolve → ForNew): the same branch, tracking ref and new-branch
// answer, so one pick never describes itself two ways at the gate and the
// creation line keeps its frozen wording. Only Path differs: a row cannot
// know the repository root the worktree goes under, and the caller's plan
// fills it.
func TestFromRowCreationMirrorsThePickPlan(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, root string) // make the row's ref exist
		pick string                          // the pick path's spelling (entryOfRow's Branch)
		row  cleanup.Row
	}{
		{
			name: "bare branch",
			seed: func(t *testing.T, root string) { git(t, root, "branch", "topic") },
			pick: "topic",
			row:  cleanup.Row{Kind: cleanup.KindBranch, Name: "topic", Branch: "topic"},
		},
		{
			name: "remote-only",
			seed: func(t *testing.T, root string) {
				git(t, root, "remote", "add", "upstream", "https://example.invalid/upstream.git")
				git(t, root, "update-ref", "refs/remotes/upstream/qa", "HEAD")
			},
			pick: "upstream/qa",
			row:  cleanup.Row{Kind: cleanup.KindRemote, Name: "qa", Branch: "qa", Remote: "upstream"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := cloneRepo(t)
			tc.seed(t, root)
			plan, err := worktree.Resolve(root, tc.pick)
			if err != nil {
				t.Fatal(err)
			}
			planned, err := ForNew(root, plan)
			if err != nil {
				t.Fatal(err)
			}
			got := FromRow(tc.row).Creation
			if got == nil || planned.Creation == nil {
				t.Fatalf("creation = %+v, plan's = %+v; both must describe the owed creation", got, planned.Creation)
			}
			if got.Branch != planned.Creation.Branch || got.Tracks != planned.Creation.Tracks || got.NewBranch != planned.Creation.NewBranch {
				t.Errorf("creation = %+v, want the plan's %+v", *got, *planned.Creation)
			}
			if got.Path != "" || planned.Creation.Path == "" {
				t.Errorf("creation path = %q (plan's %q); a row cannot know it, the plan fills it", got.Path, planned.Creation.Path)
			}
		})
	}
}
