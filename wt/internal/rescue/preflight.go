package rescue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// The limits in force: variables so a test can lower them.
var (
	limitFiles int   = MaxUntrackedFiles
	limitBytes int64 = MaxUntrackedBytes
)

// afterReadWork is a test seam, called once the source's state has been read.
var afterReadWork = func() {}

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
// DefaultBranch); with ForRoot it is the destination's directory name ("" for
// the root's branch) and the plan is Detached. A refusal is a *Refusal; any
// other error is a failure to read.
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
	// losses: raw, rescue reads what the worktree holds; a record must not hide it (a second rescue stays possible)
	p.Counts.IgnoredLeft = fresh.Losses.IgnoredEntries

	primary := false
	for _, b := range fresh.Blockers {
		switch b.Code {
		case cleanup.BlockPrimary:
			primary = true
			if e.ForRoot {
				continue
			}
			return nil, refuse(CodePrimary, "this is the repository's primary checkout, which `wt rescue` does not take; `wt restore-root` moves its branch and work into a worktree")
		case cleanup.BlockOperation:
			return nil, refuse(CodeOperation, fmt.Sprintf("an operation is in progress there (%s); finish or abort it first", cleanup.OneLine(b.Detail)))
		case cleanup.BlockHeld:
			p.Warnings = append(p.Warnings, "another wt session holds this worktree; if it edits files during the copy the rescue fails rather than copy a stale state")
		}
	}
	if e.ForRoot {
		// Root mode moves the root's own branch: a linked worktree is rescue's
		// source, and a detached root has no branch to move.
		if !primary {
			return nil, refuse(CodeNotWorktree, "root mode takes only the repository's primary checkout")
		}
		if !strings.HasPrefix(fresh.Ref, "refs/heads/") || fresh.Ref == "refs/heads/" {
			return nil, refuse(CodeNotWorktree, "a detached root has no branch to keep")
		}
	}
	// losses: raw, rescue reads what the worktree holds; a record must not hide it (a second rescue stays possible)
	if fresh.Losses.GatesUnknown {
		return nil, refuse(CodeGatesUnknown, "the worktree's state could not be read, so nothing can be copied faithfully")
	}
	// losses: raw, rescue reads what the worktree holds; a record must not hide it (a second rescue stays possible)
	if !e.ForRoot && !fresh.Losses.Dirty && fresh.Losses.Unpushed == 0 && !fresh.Losses.DetachedUnique {
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
	// losses: raw, rescue reads what the worktree holds; a record must not hide it (a second rescue stays possible)
	if fresh.Losses.Unpushed > 0 || fresh.Losses.DetachedUnique {
		n, err := sg.text("rev-list", "--count", "HEAD", "--not", "--remotes")
		if err != nil {
			return nil, err
		}
		c, err := parseCommitCount(n)
		if err != nil {
			return nil, fmt.Errorf("could not count the commits that exist only here: %w", err)
		}
		// losses: raw, rescue reads what the worktree holds; a record must not hide it (a second rescue stays possible)
		if c == 0 && fresh.Losses.Unpushed > 0 {
			// The classifier counted unpushed commits by the same definition and
			// the count says none: a fetch landed between the two reads, or one
			// of them is wrong. A prompt claiming 0 commits would mislead. A
			// detached HEAD that is only "not in the base" (DetachedUnique) can
			// legitimately count 0 here, because a remote-tracking ref already
			// holds it.
			return nil, errors.New("could not count the commits that exist only here: the classifier found some and the count found none (run it again)")
		}
		p.Counts.Commits = c
	}
	// The fingerprint is taken before the reads below, so a change made while
	// they run is never baked in as "the state that was checked"; it is taken
	// again afterwards and the two must agree.
	fp, err := takeFingerprint(sg)
	if err != nil {
		return nil, err
	}
	if err := e.readWork(sg, p); err != nil {
		return nil, err
	}
	afterReadWork()
	if again, err := takeFingerprint(sg); err != nil {
		return nil, err
	} else if d := fp.diff(again); d != "" {
		return nil, refuse(CodeSourceChanged, "the worktree changed while it was being checked ("+d+"); run it again")
	}
	p.fp = fp

	if e.ForRoot {
		// The branch is the root's own; name is the directory, which defaults
		// to the branch (the worktree package flattens its slashes).
		branch := strings.TrimPrefix(fresh.Ref, "refs/heads/")
		dir := name
		if dir == "" {
			dir = branch
		}
		dest, err := worktree.CheckNewDetached(p.Repo, dir, branch)
		if err != nil {
			return nil, refuse(CodeNameUnavailable, fmt.Sprintf("pass --to NAME to choose another directory name (%v)", err))
		}
		p.Branch, p.DirName, p.DestPath, p.Detached = branch, dir, dest, true
	} else {
		branch := name
		if branch == "" {
			branch = DefaultBranch(fresh.Ref, fresh.Path)
		}
		dest, err := worktree.CheckNew(p.Repo, branch)
		if err != nil {
			return nil, refuse(CodeNameUnavailable, fmt.Sprintf("pass --to NAME to choose another branch name (%v)", err))
		}
		p.Branch, p.DestPath = branch, dest
	}
	if w := postCheckoutHookWarning(p.Repo); w != "" {
		p.Warnings = append(p.Warnings, w)
	}
	return p, nil
}

// parseCommitCount reads `rev-list --count` output; anything but a
// non-negative integer is an error, never a silent 0.
func parseCommitCount(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("git printed %q, not a number", cleanup.OneLine(s))
	}
	return n, nil
}

// postCheckoutHookWarning says so when the repository has a post-checkout
// hook, which the new worktree's checkout does not run (hooks are disabled
// for it); "" when there is none or it cannot be looked up cheaply.
func postCheckoutHookWarning(repo string) string {
	path, err := (sgit{dir: repo}).text("rev-parse", "--git-path", "hooks/post-checkout")
	if err != nil || path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return ""
	}
	return "a post-checkout hook exists (" + cleanup.OneLine(path) + ") and did not run in the new worktree: hooks are disabled while it is created, so run whatever it sets up (dependencies, generated files) there yourself"
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
	st, err := sg.out(nil, cleanup.StatusArgs()...)
	if err != nil {
		return err
	}
	es, err := parseStatus(string(st))
	if err != nil {
		return err
	}
	var tracked []string
	var renamed []string           // en.path -> en.orig pairs flattened: [new, old, new, old...]
	worktree := map[string]int64{} // changed tracked path -> working file size
	for _, en := range es {
		if en.kind == '1' || en.kind == '2' {
			tracked = append(tracked, en.path)
			if en.orig != "" {
				tracked = append(tracked, en.orig)
				renamed = append(renamed, en.path, en.orig)
			}
			// A changed tracked file is carried as a diff held in memory and
			// written whole: it counts against the same byte limit.
			if fi, err := os.Lstat(filepath.Join(sg.dir, en.path)); err == nil && fi.Mode().IsRegular() {
				worktree[en.path] = fi.Size()
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
	// Each changed tracked file counts as the larger of its working file and
	// its index or HEAD blob (see blobSizes). A staged rename is one file: the
	// old path's HEAD blob and the new path's blob and working file count once,
	// as the larger of them, and the old path adds nothing of its own.
	blobs, err := blobSizes(sg, tracked)
	if err != nil {
		return err
	}
	origOf := map[string]bool{}
	for i := 0; i < len(renamed); i += 2 {
		origOf[renamed[i+1]] = true
		blobs[renamed[i]] = max(blobs[renamed[i]], blobs[renamed[i+1]])
	}
	for _, path := range tracked {
		if origOf[path] {
			continue
		}
		p.trackedBytes += max(worktree[path], blobs[path])
	}
	if p.trackedBytes+p.Counts.UntrackedBytes > limitBytes {
		return refuse(CodeTooLarge, tooLargeMessage())
	}
	p.tracked = tracked
	if err := checkLineEndings(sg, tracked); err != nil {
		return err
	}
	return checkContentFilters(sg, tracked)
}

// checkContentFilters refuses a changed tracked file that a content filter
// handles (a `filter` attribute: git-lfs or any clean/smudge pair). The diff
// the copy applies holds the cleaned blob but the new worktree's files would
// be smudged (or the filter missing or failing there), so verify would find a
// difference only after the branch and worktree existed.
func checkContentFilters(sg sgit, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	// The paths go on stdin (NUL-separated), never the argv: a big change set
	// would exceed ARG_MAX.
	out, err := sg.out([]byte(strings.Join(paths, "\x00")+"\x00"), "check-attr", "-z", "--stdin", "filter", "ident", "working-tree-encoding")
	if err != nil {
		return err
	}
	// -z output is NUL-terminated triples: path, attribute, value.
	f := strings.Split(string(out), "\x00")
	for i := 0; i+2 < len(f); i += 3 {
		switch v := f[i+2]; v {
		case "unspecified", "unset":
		default:
			if f[i+1] != "filter" {
				return refuse(CodeContentFilter, "the "+cleanup.OneLine(f[i+1])+" attribute rewrites "+cleanup.OneLine(f[i])+" when it is written, so a copy would not match it byte for byte")
			}
			return refuse(CodeContentFilter, "a content filter ("+cleanup.OneLine(v)+", for example git-lfs) applies to "+cleanup.OneLine(f[i])+", so a copy would not match it byte for byte")
		}
	}
	return nil
}

// checkLineEndings refuses a changed tracked file whose line endings git
// converts when the destination file is written or the source's file has
// CRLF: a text or eol attribute, core.autocrlf or core.eol. git apply and a
// fresh checkout then write different bytes than the source holds (an LF file
// becomes CRLF under core.eol=crlf with `text`), which verify would only find
// after the new worktree existed. Only the changed paths are judged, and only
// the ones affected are named.
func checkLineEndings(sg sgit, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	cfg := func(key string) string {
		v, _ := sg.text("config", "--get", key)
		return strings.ToLower(v)
	}
	autocrlf, coreEOL := cfg("core.autocrlf"), cfg("core.eol")
	convert := autocrlf == "true" || autocrlf == "input"
	// One pass over the whole index (no path arguments, so no ARG_MAX limit),
	// filtered below to the changed paths.
	want := make(map[string]bool, len(paths))
	for _, p := range paths {
		want[p] = true
	}
	out, err := sg.out(nil, "ls-files", "--eol", "-z")
	if err != nil {
		return err
	}
	recs := strings.Split(string(out), "\x00")
	for _, rec := range recs {
		info, path, ok := strings.Cut(rec, "\t")
		f := strings.Fields(info)
		if !ok || len(f) < 3 || !want[path] {
			continue
		}
		attr := strings.TrimPrefix(strings.Join(f[2:], " "), "attr/")
		crlf := false
		for _, s := range f[:2] {
			if strings.HasSuffix(s, "/crlf") || strings.HasSuffix(s, "/mixed") {
				crlf = true
			}
		}
		if strings.Contains(attr, "eol=crlf") || (crlf && (convert || attr != "")) || writesCRLF(attr, f[0], f[1], autocrlf, coreEOL) {
			return refuse(CodeLineEndings, "git converts the line endings of "+cleanup.OneLine(path)+" (a text or eol attribute, or core.autocrlf or core.eol), so a copy would not match it byte for byte")
		}
	}
	return nil
}

// writesCRLF says whether git would write this file with CRLF line endings
// into a fresh worktree: it is converted on checkout (a `text` attribute, an
// `eol` attribute, `text=auto` on content git reads as text, or core.autocrlf
// with no attribute) and the effective eol is CRLF (the eol attribute, else
// core.autocrlf=true, else core.eol=crlf; core.autocrlf overrides core.eol).
// idx and wt are the index and working-tree columns of `ls-files --eol`.
func writesCRLF(attr, idx, wt, autocrlf, coreEOL string) bool {
	var text, auto, noText bool
	eol := ""
	for _, t := range strings.Fields(attr) {
		switch {
		case t == "text":
			text = true
		case t == "-text":
			noText = true
		case t == "text=auto":
			auto = true
		case strings.HasPrefix(t, "eol="):
			eol = strings.TrimPrefix(t, "eol=")
			text = true
		}
	}
	if noText {
		return false
	}
	if !text && !auto {
		if autocrlf != "true" && autocrlf != "input" {
			return false
		}
		auto = true // core.autocrlf behaves as text=auto for a file without attributes
	}
	if !text && (idx == "i/-text" || wt == "w/-text") {
		return false // text=auto on content git reads as binary: no conversion
	}
	switch {
	case eol == "crlf":
		return true
	case eol != "":
		return false
	case autocrlf == "true":
		return true
	case autocrlf == "input":
		return false
	}
	return coreEOL == "crlf"
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
		return refuse(CodeTooLarge, tooLargeMessage())
	}
	return nil
}

func tooLargeMessage() string {
	return fmt.Sprintf("more than %d untracked files, or more than %d MiB of untracked files and changed tracked files together, would be copied; commit or move the large files first",
		limitFiles, limitBytes>>20)
}

// safeRel reports whether rel is a relative path with no "..", empty or .git
// component, so joining it under a root cannot leave the root.
func safeRel(rel string) bool {
	return cleanup.UnsafeRel(rel) == ""
}

// blobSizes is, per path, the larger of its index blob and its HEAD blob
// (0 for a path with neither). The diff a rescue carries holds those blobs in
// memory whatever the working file now is: a file staged big and then
// truncated, or a big committed file whose deletion is staged, has a small or
// missing working file. The paths are read from NUL-delimited listings of the
// index (`ls-files -s -z`, sizes by `cat-file --batch-check` over the blob ids)
// and of HEAD (`ls-tree -r -l -z`), then filtered in Go, so a path with a
// newline or any other byte is covered and no path rides an argv.
func blobSizes(sg sgit, paths []string) (map[string]int64, error) {
	want := make(map[string]bool, len(paths))
	for _, p := range paths {
		want[p] = true
	}
	sizes := make(map[string]int64, len(paths))
	if len(want) == 0 {
		return sizes, nil
	}
	// HEAD: "<mode> <type> <sha> <size>\t<path>" (size "-" for a non-blob).
	head, err := sg.out(nil, "ls-tree", "-r", "-l", "-z", "HEAD")
	if err != nil {
		return nil, err
	}
	for _, rec := range strings.Split(string(head), "\x00") {
		info, path, ok := strings.Cut(rec, "\t")
		f := strings.Fields(info)
		if !ok || len(f) != 4 || f[1] != "blob" || !want[path] {
			continue
		}
		if n, err := strconv.ParseInt(f[3], 10, 64); err == nil {
			sizes[path] = max(sizes[path], n)
		}
	}
	// Index, stage 0: "<mode> <sha> <stage>\t<path>".
	idx, err := sg.out(nil, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	var shas, owners []string
	for _, rec := range strings.Split(string(idx), "\x00") {
		info, path, ok := strings.Cut(rec, "\t")
		f := strings.Fields(info)
		if !ok || len(f) != 3 || f[2] != "0" || f[0] == "160000" || !want[path] {
			continue
		}
		shas, owners = append(shas, f[1]), append(owners, path)
	}
	if len(shas) == 0 {
		return sizes, nil
	}
	out, err := sg.out([]byte(strings.Join(shas, "\n")+"\n"), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(shas) {
		return nil, fmt.Errorf("git cat-file --batch-check: %d answers for %d requests", len(lines), len(shas))
	}
	for i, l := range lines {
		// "<sha> <type> <size>", or "<sha> missing".
		f := strings.Fields(l)
		if len(f) == 3 && f[1] == "blob" {
			if n, err := strconv.ParseInt(f[2], 10, 64); err == nil {
				sizes[owners[i]] = max(sizes[owners[i]], n)
			}
		}
	}
	return sizes, nil
}
