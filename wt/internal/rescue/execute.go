package rescue

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// afterCarry is a test seam, called once the copy is done and before the
// source is compared and the destination verified.
var afterCarry = func(*Plan) {}

// afterVerify is a test seam, called once the copy is verified and before the
// source is compared a last time and the record written.
var afterVerify = func(*Plan) {}

// beforeRecordRef is a test seam, called once the record's objects are built
// and before the source is compared a last time and the ref created.
var beforeRecordRef = func(*Plan) {}

// patchArgs make a diff reproduce content exactly: binary-safe, full ids, no
// rename guessing, none of the user's diff or textconv programs.
// The a/ b/ prefixes are set explicitly: git apply strips one leading path
// component, and diff.noprefix, diff.srcPrefix and diff.dstPrefix would
// otherwise change what the diff carries.
var patchArgs = []string{"diff", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/"}

// runState is one run's Outcome and the helpers that fill it, shared by
// Execute and ExecuteInto so both write the same Outcome shape.
type runState struct {
	o    Outcome
	p    *Plan
	dest string // the destination once it exists
}

func newRun(p *Plan) *runState { return &runState{o: Outcome{Plan: *p}, p: p} }

// fail ends the run as failed with code and the formatted reason.
func (r *runState) fail(code, format string, a ...any) Outcome {
	r.o.Status, r.o.Code, r.o.Reason = StatusFailed, code, fmt.Sprintf(format, a...)
	return r.o
}

// at records that the destination exists at dest.
func (r *runState) at(dest string) {
	r.dest = dest
	r.o.Created, r.p.DestPath, r.o.Plan.DestPath = true, dest, dest
}

// done records a finished step.
func (r *runState) done(step string) { r.o.Completed = append(r.o.Completed, step) }

// keep is fail once the destination exists: the reason says it was kept and
// where. A detached plan created no branch, so only the worktree is named.
func (r *runState) keep(code, format string, a ...any) Outcome {
	what := "the new worktree and branch were"
	if r.p.Detached {
		what = "the new worktree was"
	}
	return r.fail(code, format+" (%s kept at %s)", append(a, what, r.dest)...)
}

// Execute carries out a checked plan: it creates the branch and worktree at
// the source's HEAD, carries the staged changes, the unstaged changes and the
// untracked files, and only then claims success, after the destination's
// status and file contents match the source's. It never changes the source
// and never deletes anything. A failure before the destination exists
// creates nothing; one after it leaves the destination (Outcome.Created) and
// says so. A Detached (root mode) plan is refused as failed create_failed
// before anything is read or created: its caller creates the destination and
// runs ExecuteInto.
func (e Engine) Execute(p *Plan) Outcome {
	r := newRun(p)
	if p.Detached {
		return r.fail(CodeCreateFailed, "a root plan is executed with ExecuteInto; nothing was created")
	}
	sg := sgit{dir: p.Source.Path}

	// The state the plan was checked against must still be the source's. When
	// nothing gave anyone time to edit it since Preflight (no prompt), the
	// fingerprint Preflight took stands and this second read is skipped: the
	// comparison after the copy still catches a change.
	if !p.skipStartRecheck {
		now, err := takeFingerprint(sg)
		if err != nil {
			return r.fail(CodeUnreadable, "could not read the source: %v", err)
		}
		if d := p.fp.diff(now); d != "" {
			r.o.Status, r.o.Code = StatusRefused, CodeSourceChanged
			r.o.Reason = "the source changed since it was checked (" + d + "); nothing was created"
			return r.o
		}
	}

	dest, err := worktree.CreateAt(p.Repo, p.Branch, p.Start)
	if err != nil {
		var notMade *worktree.NotCreatedError
		var addFailed *worktree.AddFailedError
		switch {
		case errors.As(err, &notMade) && notMade.NameUnavailable:
			// The name was free at Preflight and is not now (the prompt waited):
			// whatever holds it is someone else's, not ours to point `wt remove` at.
			r.o.Status, r.o.Code = StatusRefused, CodeNameUnavailable
			r.o.Reason = fmt.Sprintf("the new branch name or directory was taken since the check (%v); pass --to NAME to choose another; nothing was created", err)
			return r.o
		case errors.As(err, &addFailed) && (addFailed.BranchLeft || addFailed.DirLeft):
			// git made the branch before it checked the worktree out, so a failure
			// can leave the branch (and a directory) behind. Say so, with the name:
			// a rerun is refused for that very name.
			r.o.BranchKept, r.o.Created = addFailed.BranchLeft, addFailed.DirLeft
			var kept []string
			if r.o.BranchKept {
				kept = append(kept, "the branch "+p.Branch)
			}
			if r.o.Created {
				kept = append(kept, "the directory "+p.DestPath)
			}
			return r.fail(CodeCreateFailed, "could not create the new worktree: %v (%s was kept; nothing was rolled back, `wt remove` can clear it)", err, strings.Join(kept, " and "))
		}
		return r.fail(CodeCreateFailed, "could not create the new worktree: %v; nothing was created", err)
	}
	r.at(dest)
	r.done("created branch " + p.Branch + " and its worktree at " + p.Start)
	return e.executeInto(r)
}

// ExecuteInto carries a checked plan into dest, a worktree its caller has
// already created at p.Start (root mode: detached, made by
// worktree.CreateDetachedAt), then verifies and records it exactly as
// Execute does after its own creation step. It does not re-check the source
// at the start (the caller does, as Execute does unless SkipStartRecheck);
// the comparison after the copy always runs. It never changes the source and
// never deletes anything; a failure keeps dest (Outcome.Created is set, since
// dest exists) and says so. A record that could not be written is a
// record_not_written warning on a moved outcome with Record nil.
func (e Engine) ExecuteInto(p *Plan, dest string) Outcome {
	r := newRun(p)
	r.at(dest)
	return e.executeInto(r)
}

// executeInto is the carry, verify and record of a run whose destination
// exists (r.dest).
func (e Engine) executeInto(r *runState) Outcome {
	p, dest, keep, done := r.p, r.dest, r.keep, r.done
	sg := sgit{dir: p.Source.Path}
	dg := sgit{dir: dest}

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
	// git apply writes a file with 0666 minus the umask, whatever mode the
	// source's copy has; give each changed tracked file the source's exact one.
	if err := setFileModes(p.Source.Path, dest, p.tracked); err != nil {
		return keep(CodeCarryFailed, "setting file modes: %v", err)
	}
	var madeDirs []string
	for _, rel := range p.untracked {
		made, err := copyUntracked(p.Source.Path, dest, rel)
		madeDirs = append(madeDirs, made...)
		if err != nil {
			// The worktree is kept (no rollback), so give the directories made
			// so far their source modes too: they are 0700 while copying. The
			// copy error is the failure; a mode that could not be set is
			// appended to it, since the kept worktree is documented to carry
			// the original's directory modes.
			msg := fmt.Sprintf("copying %s: %v", cleanup.OneLine(rel), err)
			if merr := setDirModes(p.Source.Path, dest, madeDirs); merr != nil {
				msg += fmt.Sprintf("; directory modes could not be restored: %v", merr)
			}
			return keep(CodeCarryFailed, "%s", msg)
		}
	}
	// Directory modes go on last: a read-only source directory must not block
	// writing its files into the copy.
	if err := setDirModes(p.Source.Path, dest, madeDirs); err != nil {
		return keep(CodeCarryFailed, "setting directory modes: %v", err)
	}
	if len(p.untracked) > 0 {
		done(fmt.Sprintf("copied %d untracked files", len(p.untracked)))
	}
	afterCarry(p)

	// A held session may have edited the source while it was being read.
	after, err := takeFingerprint(sg)
	if err != nil {
		return keep(CodeUnreadable, "could not re-read the source: %v", err)
	}
	if d := p.fp.diff(after); d != "" {
		return keep(CodeSourceChangedMid, "the source changed while copying (%s), so the copy may be stale", d)
	}

	// The source was just proven equal to p.fp (the diff above), so comparing
	// the destination with p.fp is comparing it with the source.
	if err := verify(sg, dg, p, madeDirs); err != nil {
		return keep(CodeVerifyFailed, "the copy does not match the source: %v", err)
	}
	done("verified status and file contents against the source")

	// The copy is verified; the record is extra proof. Its objects are built
	// first, then the source is compared with the fingerprint the copy was
	// verified against, and only then is the ref created: a change while the
	// record was read leaves no ref (the unreferenced objects are gc's).
	// Whatever stops it leaves the rescue moved and says so in a warning.
	afterVerify(p)
	o := &r.o
	warn := func(why string) {
		o.Plan.Warnings = append(o.Plan.Warnings, RecordWarningPrefix+cleanup.OneLine(why))
	}
	rec, err := buildRecord(sg, p)
	if err != nil {
		warn(err.Error())
		o.Status = StatusMoved
		return *o
	}
	beforeRecordRef(p)
	if again, err := takeFingerprint(sg); err != nil {
		warn("could not re-read the source: " + err.Error())
	} else if d := p.fp.diff(again); d != "" {
		warn("the source changed after the copy was verified (" + d + ")")
	} else if err := createRecordRef(sg, rec); err != nil {
		warn(err.Error())
	} else {
		o.Record = &rec
		done("recorded the source at " + rec.Ref)
	}
	o.Status = StatusMoved
	return *o
}

// verify checks the destination reproduces the source: the same HEAD, the
// same `status --porcelain=v2` listing and content, mode and link target at
// every path that listing names (DestinationMatches, against the fingerprint
// the caller proved equal to the source), and the same mode on every
// directory the copy created.
func verify(sg, dg sgit, p *Plan, madeDirs []string) error {
	head, err := dg.text("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != p.Start {
		return fmt.Errorf("HEAD is %s, want %s", head, p.Start)
	}
	if d, err := p.DestinationMatches(dg.dir); err != nil {
		return err
	} else if d != "" {
		return errors.New(d)
	}
	for _, rel := range madeDirs {
		sm, serr := os.Lstat(filepath.Join(sg.dir, rel))
		dm, derr := os.Lstat(filepath.Join(dg.dir, rel))
		if serr != nil || derr != nil {
			return fmt.Errorf("directory %s could not be compared (source %v, destination %v)", cleanup.OneLine(rel), serr, derr)
		}
		if sm.Mode().Perm() != dm.Mode().Perm() {
			return fmt.Errorf("directory %s has mode %o, the source's is %o", cleanup.OneLine(rel), dm.Mode().Perm(), sm.Mode().Perm())
		}
	}
	return nil
}

// DestinationMatches reads dest the way verify does and compares it with
// the source state the copy was verified against (the fingerprint Preflight
// took, which ExecuteInto proved equal to the source before and after the
// carry): the `status --porcelain=v2` listing byte for byte, and the content,
// mode and link target of every path that listing names. "" when they agree,
// else the first difference named; a failure to read dest is an error. For
// a plan with no dirt that is a clean destination. It needs no run to have
// happened and stores nothing.
func (p *Plan) DestinationMatches(dest string) (string, error) {
	st, err := sgit{dir: dest}.out(nil, statusArgs...)
	if err != nil {
		return "", err
	}
	if string(st) != p.fp.status {
		return fmt.Sprintf("git status differs:\nsource:\n%s\ndestination:\n%s", printable(p.fp.status), printable(string(st))), nil
	}
	rels := make([]string, 0, len(p.fp.files))
	for rel := range p.fp.files {
		rels = append(rels, rel)
	}
	slices.Sort(rels)
	for _, rel := range rels {
		want := p.fp.files[rel]
		if got := hashPath(dest, rel); got != want {
			return fmt.Sprintf("%s differs (source %s, destination %s)", cleanup.OneLine(rel), shortHash(want), shortHash(got)), nil
		}
	}
	return "", nil
}

// setFileModes gives each regular file in rels that exists in both trees the
// source's permission bits. A path the source no longer has (a deletion), a
// link or a directory is left as git made it; verify compares what remains.
func setFileModes(src, dst string, rels []string) error {
	for _, rel := range rels {
		fi, err := os.Lstat(filepath.Join(src, rel))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		to := filepath.Join(dst, rel)
		if ti, err := os.Lstat(to); err != nil || !ti.Mode().IsRegular() {
			continue
		}
		if err := os.Chmod(to, fi.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

// setDirModes gives each directory the copy created the mode of its source
// counterpart, deepest first so a parent made read-only does not block its
// child. A directory it cannot set (its source is gone) does not stop the
// others; the first error is returned.
func setDirModes(src, dst string, dirs []string) error {
	sorted := slices.Clone(dirs)
	slices.SortFunc(sorted, func(a, b string) int {
		return strings.Count(b, "/") - strings.Count(a, "/")
	})
	var first error
	for _, rel := range sorted {
		fi, err := os.Lstat(filepath.Join(src, rel))
		if err == nil {
			err = os.Chmod(filepath.Join(dst, rel), fi.Mode().Perm())
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
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
// followed. It returns the directories it created (relative, slash
// separated) so the caller can give them the source's modes once every file
// is in: they are made 0700 here so a read-only source directory does not
// stop its own files being written.
func copyUntracked(src, dst, rel string) (made []string, err error) {
	if !safeRel(rel) {
		return nil, fmt.Errorf("unsafe path")
	}
	parts := strings.Split(rel, "/")
	dir := dst
	for i, c := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, c)
		fi, err := os.Lstat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(dir, 0o700); err != nil {
				return made, err
			}
			made = append(made, strings.Join(parts[:i+1], "/"))
		case err != nil:
			return made, err
		case !fi.IsDir():
			return made, fmt.Errorf("%s is not a directory in the new worktree", c)
		}
	}
	from, to := filepath.Join(src, rel), filepath.Join(dst, rel)
	fi, err := os.Lstat(from)
	if err != nil {
		return made, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(from)
		if err != nil {
			return made, err
		}
		return made, os.Symlink(t, to)
	}
	if !fi.Mode().IsRegular() {
		return made, fmt.Errorf("not a regular file")
	}
	in, err := os.OpenFile(from, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return made, err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return made, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return made, err
	}
	if err := out.Close(); err != nil {
		return made, err
	}
	return made, os.Chmod(to, fi.Mode().Perm())
}
