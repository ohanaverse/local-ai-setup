package rootmove

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// ffConfig are the -c settings of the fast-forward: hooks off, as every other
// git step that changes a checkout.
var ffConfig = []string{"-c", "core.hooksPath=/dev/null"}

// fastForward is step 6: in the root (now on the default branch), `merge
// --ff-only` of origin/<Default>, only when Preflight found it a strict
// descendant. Everything here is a warning: the move is already done, so a
// skip or a failure never fails it and never undoes it (recovery row 6).
//
// A merge has no no-overwrite-ignore flag, so the files the fast-forward
// would change are first intersected with the ignored files in the root
// (compared case-folded when core.ignorecase is on); any hit skips it.
func (e Engine) fastForward(rootGit cleanup.Git, r *run) {
	p := r.p
	warn := func(format string, a ...any) {
		r.o.Plan.Warnings = append(r.o.Plan.Warnings, fmt.Sprintf(format, a...))
	}
	d := cleanup.OneLine(p.Default)
	if !p.DefaultFF {
		if p.FFNote != "" {
			warn("%s", p.FFNote)
		}
		return
	}
	remote := "refs/remotes/origin/" + p.Default
	changed, err := nulList(rootGit, "diff", "--name-only", "-z", "HEAD", remote)
	if err != nil {
		warn("%s was not fast-forwarded: could not list what it would change: %v", d, err)
		return
	}
	ignored, err := nulList(rootGit, "ls-files", "-z", "--ignored", "--exclude-standard", "--others")
	if err != nil {
		warn("%s was not fast-forwarded: could not list the ignored files: %v", d, err)
		return
	}
	fold := foldOf(rootGit)
	inIgnored := map[string]string{}
	for _, ig := range ignored {
		inIgnored[fold(ig)] = ig
	}
	var hits []string
	for _, c := range changed {
		if ig, ok := inIgnored[fold(c)]; ok {
			hits = append(hits, ig)
		}
	}
	if len(hits) > 0 {
		warn("%s was not fast-forwarded to origin/%s: it would overwrite the ignored file %s (git merge cannot be told to keep ignored files); update it yourself when ready", d, d, cleanup.OneLine(strings.Join(hits, ", ")))
		return
	}
	o, err := stepGit(rootGit, slices.Concat(ffConfig, []string{"merge", "--ff-only", remote})...)
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

// nulList runs a read whose output is NUL-separated names.
func nulList(g cleanup.Git, args ...string) ([]string, error) {
	o, err := g.Run(args...)
	if err != nil {
		return nil, err
	}
	if o.Code != 0 {
		return nil, fmt.Errorf("git %s: exit %d: %s", args[0], o.Code, cleanup.OneLine(o.Stderr))
	}
	var out []string
	for _, s := range strings.Split(o.Stdout, "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}
