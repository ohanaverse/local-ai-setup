package cleanup

import "strings"

// BranchesHolding lists the full ref names of the local and remote-tracking
// branches that contain commit (a symref like origin/HEAD does not count as
// one). Tags, the stash, rescue records and other worktrees' HEADs are not
// branches: only refs/heads and refs/remotes are asked. Reads only.
//
// It is the one place that answers "does any branch hold this commit": the
// cleanup list gathers it for a detached root (Gates.HeadHeld) and
// restore-root's Preflight and release check call it, so the list's offer and
// the command's refusal cannot drift apart (#749).
func BranchesHolding(g Git, commit string) ([]string, error) {
	text, err := g.Text("for-each-ref", "--format=%(refname)", "--contains", commit, "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l == "" || strings.HasPrefix(l, "refs/remotes/") && strings.HasSuffix(l, "/HEAD") {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}
