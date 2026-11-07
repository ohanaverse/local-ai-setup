package rescue

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// The limits in force: variables so a test can lower them.
var (
	limitFiles int   = MaxUntrackedFiles
	limitBytes int64 = MaxUntrackedBytes
)

// DefaultBranch is the name Preflight gives the new branch when none is
// asked for: rescued/<the source's branch>, or rescued/<directory name> for
// a detached worktree.
func DefaultBranch(ref, path string) string {
	if ref != "" {
		return BranchPrefix + strings.TrimPrefix(ref, "refs/heads/")
	}
	return BranchPrefix + filepath.Base(path)
}

// Preflight decides whether row's work can be copied and says exactly what
// would be. It reads only, and creates nothing: every refusal is made here,
// before anything exists. row is re-checked first (cleanup.Rescan: no fetch,
// no gh), so the facts are as of now. name is the branch to create ("" for
// DefaultBranch). A refusal is a *Refusal; any other error is a failure to
// read.
func (e Engine) Preflight(row cleanup.Row, name string) (*Plan, error) {
	switch row.Kind {
	case cleanup.KindWorktree:
	case cleanup.KindBranch:
		return nil, refuse(CodeNotWorktree, fmt.Sprintf("branch %s is checked out nowhere, so it has no uncommitted work (`wt -W %s` gives it a worktree; one stopped mid-rebase reads as detached: name it by path)",
			cleanup.OneLine(row.Name), cleanup.OneLine(row.Name)))
	default:
		return nil, refuse(CodeNotWorktree, "only a worktree's work can be copied")
	}
	fresh, err := cleanup.Rescan(e.G, e.Prior, e.In, e.Classify, cleanup.Target{Kind: cleanup.KindWorktree, Path: row.Path, Ref: row.Ref})
	if err != nil {
		return nil, fmt.Errorf("could not re-check %s: %w", cleanup.OneLine(row.Path), err)
	}
	p := &Plan{Repo: e.Prior.Repo, Source: Source{Path: fresh.Path, Ref: fresh.Ref, Head: fresh.Head}, Start: fresh.Head}
	p.Counts.IgnoredLeft = fresh.Losses.IgnoredEntries

	for _, b := range fresh.Blockers {
		switch b.Code {
		case cleanup.BlockPrimary:
			return nil, refuse(CodePrimary, "this is the repository's primary checkout; moving its work off it is a separate step (#522)")
		case cleanup.BlockOperation:
			return nil, refuse(CodeOperation, fmt.Sprintf("an operation is in progress there (%s); finish or abort it first", cleanup.OneLine(b.Detail)))
		case cleanup.BlockHeld:
			p.Warnings = append(p.Warnings, "another wt session holds this worktree; if it edits files during the copy the rescue fails rather than copy a stale state")
		}
	}
	if fresh.Losses.GatesUnknown {
		return nil, refuse(CodeGatesUnknown, "the worktree's state could not be read, so nothing can be copied faithfully")
	}
	if !fresh.Losses.Dirty && fresh.Losses.Unpushed == 0 && !fresh.Losses.DetachedUnique {
		return nil, refuse(CodeNothingToKeep, "nothing to keep: no uncommitted changes and no commits that exist only here")
	}
	if !cleanup.IsFullCommitID(fresh.Head) {
		return nil, refuse(CodeUnreadable, "the worktree's HEAD is not a commit")
	}

	sg := sgit{dir: fresh.Path}
	if head, err := sg.text("rev-parse", "HEAD"); err != nil {
		return nil, err
	} else if head != fresh.Head {
		return nil, refuse(CodeSourceChanged, "the worktree's HEAD moved while it was being checked; run it again")
	}
	// Commits that exist on no remote-tracking ref: the classifier's own
	// "unpushed" definition, counted for a detached HEAD too.
	if fresh.Losses.Unpushed > 0 || fresh.Losses.DetachedUnique {
		n, err := sg.text("rev-list", "--count", "HEAD", "--not", "--remotes")
		if err != nil {
			return nil, err
		}
		fmt.Sscan(n, &p.Counts.Commits)
	}
	if err := e.readWork(sg, p); err != nil {
		return nil, err
	}

	branch := name
	if branch == "" {
		branch = DefaultBranch(fresh.Ref, fresh.Path)
	}
	dest, err := worktree.CheckNew(p.Repo, branch)
	if err != nil {
		return nil, refuse(CodeNameUnavailable, fmt.Sprintf("pass --to NAME to choose another branch name (%v)", err))
	}
	p.Branch, p.DestPath = branch, dest

	if p.fp, err = takeFingerprint(sg, "refs/heads/"+branch); err != nil {
		return nil, err
	}
	return p, nil
}

// readWork reads the source's uncommitted state into p and refuses what has
// no faithful copy: unmerged paths, skip-worktree and assume-unchanged
// entries, submodule changes, intent-to-add entries, a path both staged and
// modified, a nested repository, a fifo or other special file, and more than
// the size limits.
func (e Engine) readWork(sg sgit, p *Plan) error {
	if u, err := sg.out(nil, "ls-files", "-u", "-z"); err != nil {
		return err
	} else if len(u) > 0 {
		return refuse(CodeUnmerged, "the index has unmerged paths; resolve them first")
	}
	v, err := sg.out(nil, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, t := range strings.Split(string(v), "\x00") {
		if t != "" && (t[0] == 'S' || (t[0] >= 'a' && t[0] <= 'z')) {
			return refuse(CodeHiddenIndex, "a file is marked skip-worktree or assume-unchanged, so its edits are invisible to git and cannot be copied: "+cleanup.OneLine(strings.TrimLeft(t[1:], " ")))
		}
	}
	st, err := sg.out(nil, statusArgs...)
	if err != nil {
		return err
	}
	es, err := parseStatus(string(st))
	if err != nil {
		return err
	}
	var tracked []string
	for _, en := range es {
		if en.kind == '1' || en.kind == '2' {
			tracked = append(tracked, en.path)
			if en.orig != "" {
				tracked = append(tracked, en.orig)
			}
		}
		if en.kind == 'u' {
			return refuse(CodeUnmerged, "the index has unmerged paths; resolve them first")
		}
		if en.kind == '?' {
			if err := e.checkUntracked(sg.dir, en.path, p); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(en.sub, "S") {
			return refuse(CodeSubmodule, "a submodule has changes, which cannot be copied: "+cleanup.OneLine(en.path))
		}
		x, y := en.xy[0], en.xy[1]
		switch {
		case y == 'A':
			return refuse(CodeIntentToAdd, "a file is marked intent-to-add (git add -N): "+cleanup.OneLine(en.path))
		case x != '.' && y != '.':
			return refuse(CodeStagedAndEdited, "a file is both staged and modified: "+cleanup.OneLine(en.path)+" (stage it or restore it first)")
		}
		if x != '.' {
			p.Counts.Staged++
		}
		if y != '.' {
			p.Counts.Unstaged++
		}
	}
	return checkLineEndings(sg, tracked)
}

// checkLineEndings refuses a changed tracked file whose line endings git
// converts (a text or eol attribute, or core.autocrlf) when the file has CRLF
// or the attribute asks for it. git apply and a fresh checkout then write
// different bytes than the source holds, which verify would only find after
// the new worktree existed.
func checkLineEndings(sg sgit, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	autocrlf, _ := sg.text("config", "--get", "core.autocrlf")
	autocrlf = strings.ToLower(autocrlf)
	convert := autocrlf == "true" || autocrlf == "input"
	args := append([]string{"--literal-pathspecs", "ls-files", "--eol", "-z", "--"}, paths...)
	out, err := sg.out(nil, args...)
	if err != nil {
		return err
	}
	for _, rec := range strings.Split(string(out), "\x00") {
		info, path, ok := strings.Cut(rec, "\t")
		f := strings.Fields(info)
		if !ok || len(f) < 3 {
			continue
		}
		attr := strings.TrimPrefix(strings.Join(f[2:], " "), "attr/")
		crlf := false
		for _, s := range f[:2] {
			if strings.HasSuffix(s, "/crlf") || strings.HasSuffix(s, "/mixed") {
				crlf = true
			}
		}
		if strings.Contains(attr, "eol=crlf") || (crlf && (convert || attr != "")) {
			return refuse(CodeLineEndings, "git converts the line endings of "+cleanup.OneLine(path)+" (a text or eol attribute, or core.autocrlf), so a copy would not match it byte for byte")
		}
	}
	return nil
}

// checkUntracked refuses an untracked path that cannot be copied faithfully
// and otherwise adds it to the plan.
func (e Engine) checkUntracked(root, rel string, p *Plan) error {
	if strings.HasSuffix(rel, "/") {
		return refuse(CodeNestedRepo, "a nested git repository cannot be copied: "+cleanup.OneLine(rel))
	}
	if !safeRel(rel) {
		return refuse(CodeSpecialFile, "an untracked path is not safe to copy: "+cleanup.OneLine(rel))
	}
	fi, err := os.Lstat(filepath.Join(root, rel))
	if err != nil {
		return fmt.Errorf("reading %s: %w", cleanup.OneLine(rel), err)
	}
	switch m := fi.Mode(); {
	case m&os.ModeSymlink != 0:
	case m.IsRegular():
		p.Counts.UntrackedBytes += fi.Size()
	default:
		return refuse(CodeSpecialFile, fmt.Sprintf("%s is not a regular file or symlink (%v)", cleanup.OneLine(rel), m.Type()))
	}
	p.untracked = append(p.untracked, rel)
	p.Counts.Untracked++
	if p.Counts.Untracked > limitFiles || p.Counts.UntrackedBytes > limitBytes {
		return refuse(CodeTooLarge, fmt.Sprintf("more than %d untracked files or %d MiB would be copied; commit or move the large files first",
			limitFiles, limitBytes>>20))
	}
	return nil
}

// safeRel reports whether rel is a relative path with no "..", empty or .git
// component, so joining it under a root cannot leave the root.
func safeRel(rel string) bool {
	if rel == "" || filepath.IsAbs(rel) || strings.ContainsRune(rel, 0) {
		return false
	}
	return !slices.ContainsFunc(strings.Split(rel, "/"), func(c string) bool {
		return c == "" || c == ".." || c == "." || strings.EqualFold(c, ".git")
	})
}
