package rootmove

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// fastForward is step 6: in the root (now on the default branch), `merge
// --ff-only` of origin/<Default>, only when Preflight found it a strict
// descendant. Everything here is a warning: the move is already done, so a
// skip or a failure never fails it and never undoes it (recovery row 6).
//
// A merge has no no-overwrite-ignore flag, so the files the fast-forward
// would change are first intersected with the ignored files in the root
// (compared case-folded when core.ignorecase is on), counting an ignored file
// at a parent prefix of a changed path too; any hit skips it.
func (e Engine) fastForward(rootGit cleanup.Git, r *run) {
	p := r.p
	warn := func(format string, a ...any) {
		r.o.Plan.Warnings = append(r.o.Plan.Warnings, fmt.Sprintf(format, a...))
	}
	if !p.DefaultFF {
		switch {
		case p.FFBenign:
			r.done(p.FFNote)
		case p.FFNote != "":
			warn("%s", p.FFNote)
		}
		return
	}
	d := cleanup.OneLine(p.Default)
	remote := "refs/remotes/origin/" + p.Default
	changed, err := rootGit.Lines("diff", "--name-only", "-z", "HEAD", remote)
	if err != nil {
		warn("%s was not fast-forwarded: could not list what it would change: %v", d, err)
		return
	}
	var hits []string
	if len(changed) > 0 {
		// Nothing changed means nothing to collide with: the scan is skipped.
		hits, err = ignoredCollisions(rootGit, changed)
		if err != nil {
			warn("%s was not fast-forwarded: could not list the ignored files: %v", d, err)
			return
		}
	}
	if len(hits) > 0 {
		warn("%s was not fast-forwarded to origin/%s: it would overwrite or remove the ignored file %s (git merge cannot be told to keep ignored files); update it yourself when ready", d, d, cleanup.OneLine(strings.Join(hits, ", ")))
		return
	}
	o, err := e.Hooks.step(rootGit, slices.Concat(cleanup.WriteConfig(), []string{"merge", "--ff-only", remote})...)
	switch {
	case err != nil:
		warn("%s was not fast-forwarded: git merge --ff-only: %v", d, err)
	case o.Code != 0:
		warn("%s was not fast-forwarded: git merge --ff-only: exit %d: %s", d, o.Code, cleanup.OneLine(o.Stderr))
	default:
		r.o.FastForwarded = true
		r.done("fast-forwarded " + p.Default + " to origin/" + p.Default)
	}
}

// ignoredCollisions returns the ignored files of the root that merging the
// changed paths would overwrite or remove (core.ignorecase is read here).
func ignoredCollisions(rootGit cleanup.Git, changed []string) ([]string, error) {
	ignored, err := rootGit.Lines("ls-files", "-z", "--ignored", "--exclude-standard", "--others")
	if err != nil {
		return nil, err
	}
	fold := cleanup.PathFold(rootGit)
	// inIgnored: the ignored files. ignoredUnder: the directories that hold an
	// ignored file: the merge writes the changed paths as files, so a changed
	// path that is one of these directories would replace the directory and
	// delete the ignored file in it.
	inIgnored, ignoredUnder := cleanup.PathSets(ignored, fold)
	var hits []string
	// A changed path collides with an ignored file at the path itself or at
	// any parent prefix (an ignored file `sub` where origin adds `sub/f`:
	// merge would replace the file by a directory), and with an ignored file
	// under it (the root holds an ignored `build/x.o` and origin adds the
	// file `build`: merge would replace the directory by the file).
	for _, c := range changed {
		for q := c; q != "." && q != "/" && q != ""; q = path.Dir(q) {
			if ig, ok := inIgnored[fold(q)]; ok {
				hits = append(hits, ig)
				break
			}
		}
		if ig, ok := ignoredUnder[fold(c)]; ok {
			hits = append(hits, ig)
		}
	}
	return hits, nil
}
