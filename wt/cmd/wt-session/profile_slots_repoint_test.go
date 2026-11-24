package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// repointServer is slotsServer plus the alias rows of a rename: alias/new and
// alias/old (a redirect to it).
func repointServer(t *testing.T) *fakeModelServer {
	t.Helper()
	f := slotsServer(t)
	row := func(id, redirect string) modelapi.Row {
		r := wireRow(id, modelapi.ActionLaunch, 50)
		r.ProviderID, r.Family, r.Kind, r.RedirectTo = "alias", "alias", modelapi.KindAlias, redirect
		return r
	}
	f.Rows = append(f.Rows, row("alias/new", ""), row("alias/old", "alias/new"))
	return f
}

const repointFile = `[[profile]]
name = "base"
agent = "claude"
default = "sonnet"

[profile.slots]
sonnet = "alias/old"
haiku = "openrouter/b"

[[profile]]
name = "other"
agent = "claude"
default = "sonnet"

[profile.slots]
sonnet = "openrouter/a"
`

// TestSlotsRepointMovesProfilesAndIsIdempotent pins #687 decision 4 end to
// end: the dry run names the profiles and writes nothing; the real run
// rewrites only the matching slot lines in one backed-up write and prints the
// profiles changed; a second run says `no change` and writes no second
// backup. Profiles on other models are untouched.
func TestSlotsRepointMovesProfilesAndIsIdempotent(t *testing.T) {
	a := pointAtFake(t, repointServer(t), "")
	if err := os.MkdirAll(filepath.Dir(profiles.SlotPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profiles.SlotPath(), []byte(repointFile), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runProfileCmd(t, a, "slots", "repoint", "alias/old", "alias/new", "--dry-run")
	if err != nil || !strings.Contains(out, "would repoint alias/old -> alias/new in: base") || readSlotFile(t) != repointFile {
		t.Fatalf("dry run: err %v out %q", err, out)
	}
	if m, _ := filepath.Glob(profiles.SlotPath() + ".bak-*"); len(m) != 0 {
		t.Errorf("a dry run wrote a backup: %v", m)
	}
	out, err = runProfileCmd(t, a, "slots", "repoint", "alias/old", "alias/new")
	if err != nil {
		t.Fatalf("repoint: %v\n%s", err, out)
	}
	want := strings.Replace(repointFile, `sonnet = "alias/old"`, `sonnet = "alias/new"`, 1)
	if readSlotFile(t) != want || !strings.Contains(out, "backup: ") || !strings.Contains(out, "repointed alias/old -> alias/new in: base") {
		t.Fatalf("file:\n%s\nout %q", readSlotFile(t), out)
	}
	backups, _ := filepath.Glob(profiles.SlotPath() + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("backups %v, want one", backups)
	}
	out, err = runProfileCmd(t, a, "slots", "repoint", "alias/old", "alias/new")
	if err != nil || !strings.HasPrefix(out, "no change: no slot profile uses alias/old") {
		t.Fatalf("second run: err %v out %q", err, out)
	}
	if again, _ := filepath.Glob(profiles.SlotPath() + ".bak-*"); len(again) != 1 {
		t.Errorf("the no-op run wrote a backup: %v", again)
	}
}

// TestSlotsRepointRefusals pins what repoint refuses before writing: ids that
// are not aliases, the same id twice, a new id the server does not serve, and
// a new id that is itself a redirect. The file stays as it was in each case.
func TestSlotsRepointRefusals(t *testing.T) {
	a := pointAtFake(t, repointServer(t), "")
	if err := os.MkdirAll(filepath.Dir(profiles.SlotPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profiles.SlotPath(), []byte(repointFile), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"openrouter/a", "alias/new"}, "is not an alias id"},
		{[]string{"alias/old", "openrouter/a"}, "is not an alias id"},
		{[]string{"alias/old", "alias/old"}, "both the old and the new id"},
		{[]string{"alias/old", "alias/ghost"}, "not a model or alias on the model server"},
	} {
		_, err := runProfileCmd(t, a, append([]string{"slots", "repoint"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) || readSlotFile(t) != repointFile {
			t.Errorf("%v: err %v, want %q", tc.args, err, tc.want)
		}
	}
	// alias/new redirecting elsewhere: repoint to the end of the chain.
	f := repointServer(t)
	f.Rows[len(f.Rows)-2].RedirectTo = "alias/final"
	a = pointAtFake(t, f, "") // a new config home: the file goes back in
	if err := os.MkdirAll(filepath.Dir(profiles.SlotPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profiles.SlotPath(), []byte(repointFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runProfileCmd(t, a, "slots", "repoint", "alias/old", "alias/new"); err == nil || !strings.Contains(err.Error(), "itself a redirect to alias/final") {
		t.Errorf("repoint to a redirect: %v", err)
	}
}
