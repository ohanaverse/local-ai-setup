package rescue

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/dirtsnap"
)

// recordEnv is the identity every record commit carries, so the record never
// depends on the user's git config (which may be unset).
var recordEnv = []string{
	"GIT_AUTHOR_NAME=wt rescue", "GIT_AUTHOR_EMAIL=wt@invalid",
	"GIT_COMMITTER_NAME=wt rescue", "GIT_COMMITTER_EMAIL=wt@invalid",
}

// hasControl reports a rune that cannot sit in a commit trailer.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// writeRecord writes the two commits that prove a rescue and the ref that
// keeps them: I (the source's index tree, parent the source's HEAD) and W
// (the source's working-tree tree, parent I). It only adds objects and one
// create-only ref; the source is read.
func writeRecord(src sgit, p *Plan) (Record, error) {
	srcPath := cleanup.ResolvePath(p.Source.Path)
	refName := p.Source.Ref
	if refName == "" {
		refName = "detached"
	}
	for _, v := range []string{srcPath, refName, p.Branch, p.DestPath} {
		if hasControl(v) {
			return Record{}, fmt.Errorf("a control character in %q cannot be written as a trailer", v)
		}
	}
	msg := "wt rescue record\n\n" + strings.Join([]string{
		"Wt-Version: 1",
		"Wt-Source-Path: " + srcPath,
		"Wt-Source-Ref: " + refName,
		"Wt-Source-Head: " + p.Source.Head,
		"Wt-Dest-Branch: " + p.Branch,
		"Wt-Dest-Path: " + p.DestPath,
	}, "\n") + "\n"

	itree, err := dirtsnap.IndexTree(src.dir, src.out, true)
	if err != nil {
		return Record{}, err
	}
	wtree, err := dirtsnap.Build(src.dir, src.out, true, dirtsnap.Limits{})
	if err != nil {
		return Record{}, err
	}
	ci := sgit{dir: src.dir, env: recordEnv}
	commit := func(tree, parent string) (string, error) {
		return ci.text2(msg, "commit-tree", tree, "-p", parent, "-F", "-")
	}
	i, err := commit(itree, p.Source.Head)
	if err != nil {
		return Record{}, err
	}
	w, err := commit(wtree, i)
	if err != nil {
		return Record{}, err
	}
	if len(w) < 16 {
		return Record{}, fmt.Errorf("unexpected commit id %q", w)
	}
	ref := RecordRefPrefix + w[:16]
	if err := updateRecordRef(src, ref, w); err != nil {
		return Record{}, err
	}
	return Record{Ref: ref, Commit: w}, nil
}

// updateRecordRef creates ref at commit and nothing else: an existing ref at
// the same commit counts as written, at another commit it is an error. It is
// a variable so a test can fail the one step.
var updateRecordRef = func(src sgit, ref, commit string) error {
	_, err := src.out(nil, "update-ref", ref, commit, "")
	if err == nil {
		return nil
	}
	if cur, e := src.text("rev-parse", "--verify", "-q", ref); e == nil && cur == commit {
		return nil
	}
	return err
}
