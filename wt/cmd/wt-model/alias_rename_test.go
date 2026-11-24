package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
)

// unreadSessions is a clientSessions seam whose read fails, as a running
// server's does when its sessions cannot be listed.
func unreadSessions(_ context.Context, _ io.Writer, _ servestate.Store, unread *error) func() map[string]int {
	return func() map[string]int {
		if unread != nil {
			*unread = errors.New("tls: failed to verify certificate")
		}
		return nil
	}
}

// aliasRegistryWithOld is aliasRegistry plus one two-target alias to rename.
const aliasRegistryWithOld = aliasRegistry + `
[[aliases]]
id = "alias/old"
targets = [{ model = "openrouter/big" }, { model = "openrouter/small", order = 2 }]
`

// TestAliasRenameKeepsBothIdsRoutedAndEndsWithTheRepointLine pins #687
// decision 2 end to end: one rename writes alias/new and turns alias/old into
// a redirect, the one route sync leaves BOTH ids with a row per target (so a
// running session on the old id never loses its route), the output ends with
// the repoint command for the session machines, and no printed line carries
// the cloud key. If the sync dropped the old id's rows, sessions pinned to it
// would break at the rename.
func TestAliasRenameKeepsBothIdsRoutedAndEndsWithTheRepointLine(t *testing.T) {
	h, fake, path := aliasEnv(t, aliasRegistryWithOld)
	if _, _, err := execAlias(t, h, "litellm", "sync"); err != nil || len(aliasRows(fake, "alias/old")) != 2 {
		t.Fatalf("setup sync: %v rows %v", err, fake.ids())
	}
	out, errOut, err := execAlias(t, h, "alias", "rename", "alias/old", "alias/new")
	if err != nil {
		t.Fatalf("rename: %v\n%s%s", err, out, errOut)
	}
	const last = "next, on each machine that launches slot profiles: wt profile slots repoint alias/old alias/new\n"
	if !strings.HasPrefix(out, "renamed alias: alias/old -> alias/new\n") || !strings.HasSuffix(out, last) {
		t.Errorf("stdout:\n%s", out)
	}
	reg := mustRead(t, path)
	if !strings.Contains(reg, "id = \"alias/old\"\nredirect_to = \"alias/new\"\n") || !strings.Contains(reg, "id = \"alias/new\"\ntargets") {
		t.Errorf("registry:\n%s", reg)
	}
	if len(aliasRows(fake, "alias/old")) != 2 || len(aliasRows(fake, "alias/new")) != 2 {
		t.Errorf("rows after rename: old %v new %v, want two each", aliasRows(fake, "alias/old"), aliasRows(fake, "alias/new"))
	}
	if strings.Contains(out+errOut, aliasKeySentinel) {
		t.Error("the rename printed a resolved key")
	}
	// The sweep: kept while a session uses the redirect, removed once none does.
	stubSessions(h, map[string]int{"alias/old": 1})
	out, _, err = execAlias(t, h, "alias", "rm", "--finished", "--yes")
	if err != nil || !strings.Contains(out, "kept redirect: alias/old (used by 1 client session(s))") || !strings.Contains(out, "no finished redirects") || !strings.Contains(mustRead(t, path), "redirect_to") {
		t.Fatalf("sweep with a session: err %v stdout %q", err, out)
	}
	stubSessions(h, map[string]int{})
	out, _, err = execAlias(t, h, "alias", "rm", "--finished", "--yes")
	if err != nil || !strings.HasPrefix(out, "removed alias: alias/old\n") || strings.Contains(mustRead(t, path), "redirect_to") {
		t.Fatalf("sweep: err %v stdout %q", err, out)
	}
	if len(aliasRows(fake, "alias/old")) != 0 || len(aliasRows(fake, "alias/new")) != 2 {
		t.Errorf("rows after the sweep: old %v new %v", aliasRows(fake, "alias/old"), aliasRows(fake, "alias/new"))
	}
}

// TestAliasRenameDryRunAndRefusals pins that --dry-run prints the plan and
// the repoint line and writes nothing, and that each refusal (unknown old, new
// exists, rename of a redirect, an old id a redirect points at) is an error
// that changes nothing.
func TestAliasRenameDryRunAndRefusals(t *testing.T) {
	h, fake, path := aliasEnv(t, aliasRegistryWithOld)
	before := mustRead(t, path)
	out, _, err := execAlias(t, h, "alias", "rename", "alias/old", "alias/new", "--dry-run")
	if err != nil || !strings.Contains(out, "would rename alias: alias/old -> alias/new\n") || !strings.Contains(out, "  target openrouter/big order 1\n") ||
		!strings.HasSuffix(out, "wt profile slots repoint alias/old alias/new\n") || mustRead(t, path) != before || len(fake.ids()) != 0 {
		t.Fatalf("dry run: err %v stdout:\n%s", err, out)
	}
	if _, _, err := execAlias(t, h, "alias", "rename", "alias/old", "alias/new"); err != nil {
		t.Fatal(err)
	}
	before = mustRead(t, path)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"alias/zzz", "alias/n"}, "alias not found"},
		{[]string{"alias/old", "alias/n"}, "already a redirect"},
		{[]string{"alias/new", "alias/old"}, "alias already exists"},
		{[]string{"alias/new", "alias/n"}, "redirect target of alias/old"},
	} {
		_, _, err := execAlias(t, h, append([]string{"alias", "rename"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) || mustRead(t, path) != before {
			t.Errorf("rename %v: err %v, want %q and no change", tc.args, err, tc.want)
		}
	}
	// A plain rm of the redirect target is refused while the redirect stays.
	if _, _, err := execAlias(t, h, "alias", "rm", "alias/new", "--yes"); err == nil || !strings.Contains(err.Error(), "redirect target of alias/old") {
		t.Errorf("rm of a redirect target: %v", err)
	}
}

// TestAliasRmFinishedRefusesWhenSessionsUnreadable pins the sweep's guard: a
// running server whose sessions cannot be read leaves every redirect possibly
// in use, so --finished refuses with exit 3 and removes nothing, and
// --force-in-use is the way past it.
func TestAliasRmFinishedRefusesWhenSessionsUnreadable(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryWithOld)
	if _, _, err := execAlias(t, h, "alias", "rename", "alias/old", "alias/new"); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, path)
	h.clientSessions = unreadSessions
	_, _, err := execAlias(t, h, "alias", "rm", "--finished", "--yes")
	if err == nil || exitcode.Of(err) != 3 || !strings.Contains(err.Error(), "--force-in-use") || mustRead(t, path) != before {
		t.Fatalf("err %v (exit %d)", err, exitcode.Of(err))
	}
	if _, _, err := execAlias(t, h, "alias", "rm", "--finished", "--yes", "--force-in-use"); err != nil || strings.Contains(mustRead(t, path), "redirect_to") {
		t.Fatalf("with --force-in-use: %v", err)
	}
	if _, _, err := execAlias(t, h, "alias", "rm", "--finished", "alias/x"); err == nil {
		t.Error("--finished with an id was accepted")
	}
}

// TestAliasListShowsTheRedirectColumnOnlyWhenOneExists pins #687's table: a
// redirect lists the effective targets and `-> alias/new` in a REDIRECT
// column, and a registry with no redirect keeps the five-column table
// scripts already parse.
func TestAliasListShowsTheRedirectColumnOnlyWhenOneExists(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistryWithOld)
	out, _, err := execAlias(t, h, "alias", "list")
	if err != nil || strings.Contains(out, "REDIRECT") {
		t.Fatalf("no redirect: err %v\n%s", err, out)
	}
	if _, _, err := execAlias(t, h, "alias", "rename", "alias/old", "alias/new"); err != nil {
		t.Fatal(err)
	}
	out, _, err = execAlias(t, h, "alias", "list")
	if err != nil || !strings.Contains(out, "REDIRECT") || !strings.Contains(out, "-> alias/new") ||
		strings.Count(out, "1:openrouter/big,2:openrouter/small") != 2 {
		t.Fatalf("err %v\n%s", err, out)
	}
}
