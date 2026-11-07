package rescue

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// afterCarry is a test seam, called once the copy is done and before the
// source is compared and the destination verified.
var afterCarry = func(*Plan) {}

// patchArgs make a diff reproduce content exactly: binary-safe, full ids, no
// rename guessing, none of the user's diff or textconv programs.
// The a/ b/ prefixes are set explicitly: git apply strips one leading path
// component, and diff.noprefix, diff.srcPrefix and diff.dstPrefix would
// otherwise change what the diff carries.
var patchArgs = []string{"diff", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/"}

// Execute carries out a checked plan: it creates the branch and worktree at
// the source's HEAD, carries the staged changes, the unstaged changes and the
// untracked files, and only then claims success, after the destination's
// status and file contents match the source's. It never changes the source
// and never deletes anything. A failure before the destination exists
// creates nothing; one after it leaves the destination (Outcome.Created) and
// says so.
func (e Engine) Execute(p *Plan) Outcome {
	o := Outcome{Plan: *p}
	fail := func(code, format string, a ...any) Outcome {
		o.Status, o.Code, o.Reason = StatusFailed, code, fmt.Sprintf(format, a...)
		return o
	}
	sg := sgit{dir: p.Source.Path}
	exclude := "refs/heads/" + p.Branch

	// The state the plan was checked against must still be the source's.
	now, err := takeFingerprint(sg, exclude)
	if err != nil {
		return fail(CodeUnreadable, "could not read the source: %v", err)
	}
	if d := p.fp.diff(now); d != "" {
		o.Status, o.Code = StatusRefused, CodeSourceChanged
		o.Reason = "the source changed since it was checked (" + d + "); nothing was created"
		return o
	}

	dest, err := worktree.CreateAt(p.Repo, p.Branch, p.Start)
	if err != nil {
		return fail(CodeCreateFailed, "could not create the new worktree: %v; nothing was created", err)
	}
	o.Created, p.DestPath, o.Plan.DestPath = true, dest, dest
	done := func(step string) { o.Completed = append(o.Completed, step) }
	done("created branch " + p.Branch + " and its worktree at " + p.Start)
	dg := sgit{dir: dest}
	keep := func(code, format string, a ...any) Outcome {
		return fail(code, format+" (the new worktree and branch were kept at %s)", append(a, dest)...)
	}

	// Staged changes: HEAD to index, applied to dest's index and files.
	staged, err := sg.out(nil, append(patchArgs, "--cached")...)
	if err != nil {
		return keep(CodeCarryFailed, "reading the staged changes: %v", err)
	}
	if len(staged) > 0 {
		if _, err := dg.out(staged, "apply", "--index", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return keep(CodeCarryFailed, "applying the staged changes: %v", err)
		}
		done("carried staged changes")
	}
	// Unstaged changes: index to files, applied to dest's files only.
	unstaged, err := sg.out(nil, patchArgs...)
	if err != nil {
		return keep(CodeCarryFailed, "reading the unstaged changes: %v", err)
	}
	if len(unstaged) > 0 {
		if _, err := dg.out(unstaged, "apply", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return keep(CodeCarryFailed, "applying the unstaged changes: %v", err)
		}
		done("carried unstaged changes")
	}
	for _, rel := range p.untracked {
		if err := copyUntracked(p.Source.Path, dest, rel); err != nil {
			return keep(CodeCarryFailed, "copying %s: %v", cleanup.OneLine(rel), err)
		}
	}
	if len(p.untracked) > 0 {
		done(fmt.Sprintf("copied %d untracked files", len(p.untracked)))
	}
	afterCarry(p)

	// A held session may have edited the source while it was being read.
	after, err := takeFingerprint(sg, exclude)
	if err != nil {
		return keep(CodeUnreadable, "could not re-read the source: %v", err)
	}
	if d := p.fp.diff(after); d != "" {
		return keep(CodeSourceChangedMid, "the source changed while copying (%s), so the copy may be stale", d)
	}

	if err := verify(sg, dg, p, after); err != nil {
		return keep(CodeVerifyFailed, "the copy does not match the source: %v", err)
	}
	done("verified status and file contents against the source")
	o.Status = StatusMoved
	return o
}

// verify checks the destination reproduces the source: the same HEAD, the
// same `status --porcelain=v2` listing byte for byte, and the same content,
// mode and link target at every path that listing names.
func verify(sg, dg sgit, p *Plan, src fingerprint) error {
	head, err := dg.text("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != p.Start {
		return fmt.Errorf("HEAD is %s, want %s", head, p.Start)
	}
	st, err := dg.out(nil, statusArgs...)
	if err != nil {
		return err
	}
	if string(st) != src.status {
		return fmt.Errorf("git status differs:\nsource:\n%s\ndestination:\n%s", printable(src.status), printable(string(st)))
	}
	for rel, want := range src.files {
		if got := hashPath(dg.dir, rel); got != want {
			return fmt.Errorf("%s differs (source %s, destination %s)", cleanup.OneLine(rel), shortHash(want), shortHash(got))
		}
	}
	return nil
}

func printable(s string) string { return strings.ReplaceAll(s, "\x00", "\n") }

func shortHash(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// copyUntracked copies one untracked file or symlink from src to dst under
// the same relative path. It creates the missing parent directories one by
// one, refusing a parent that is a symlink or not a directory (so a link
// cannot send the copy out of dst); it opens the file exclusively (never
// overwriting) and keeps its permission bits; a symlink is recreated, not
// followed.
func copyUntracked(src, dst, rel string) error {
	if !safeRel(rel) {
		return fmt.Errorf("unsafe path")
	}
	parts := strings.Split(rel, "/")
	dir := dst
	for _, c := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, c)
		fi, err := os.Lstat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory in the new worktree", c)
		}
	}
	from, to := filepath.Join(src, rel), filepath.Join(dst, rel)
	fi, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return os.Symlink(t, to)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	in, err := os.OpenFile(from, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(to, fi.Mode().Perm())
}
