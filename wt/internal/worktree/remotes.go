package worktree

import (
	"net/url"
	"strings"
)

// repoFromURL returns the lower-cased "owner/repo" a git remote URL names,
// with any ".git" suffix stripped. It reads https://host/o/r(.git),
// ssh://git@host/o/r.git and scp-style git@host:o/r.git, and returns ""
// when it cannot tell (a local path, an empty string).
//
// The host is ignored by design: a GitHub Enterprise host carrying the same
// owner/repo as the base repository would compare equal. That is a known
// limit, accepted because the comparison only decides whether to show a PR
// tag.
func repoFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	var path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		// A file:// remote is a local clone, like a plain path: its last
		// two segments name a directory, not an owner/repo.
		if u.Scheme == "file" || u.Host == "" {
			return ""
		}
		path = u.Path
	case strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "."):
		return ""
	default:
		// scp-style: [user@]host:owner/repo.git
		i := strings.IndexByte(raw, ':')
		if i < 0 {
			return ""
		}
		path = raw[i+1:]
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	owner, repo := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || repo == "" {
		return ""
	}
	return strings.ToLower(owner + "/" + repo)
}

// RemoteRepos maps each remote's name to the owner/repo of its URL ("" when
// the URL does not name one). It asks `git remote get-url`, which applies
// insteadOf rewrites. A failed `git remote` yields an empty map; a failed
// get-url leaves just that remote out.
func RemoteRepos(dir string) map[string]string {
	repos := map[string]string{}
	out, err := runGit(dir, "remote")
	if err != nil {
		return repos
	}
	for _, name := range strings.Fields(string(out)) {
		u, err := runGit(dir, "remote", "get-url", name)
		if err != nil {
			continue
		}
		repos[name] = repoFromURL(string(u))
	}
	return repos
}

// SplitRemoteBranch splits a remote-tracking short name such as
// "keith/feat/x" into its remote and branch. The longest remote name in
// remotes that is followed by "/" wins, since remote names may contain
// slashes. ok is false when no known remote matches: the first slash is not
// guessed.
func SplitRemoteBranch(remoteBranch string, remotes map[string]string) (remote, branch string, ok bool) {
	for r := range remotes {
		if len(r) > len(remote) && strings.HasPrefix(remoteBranch, r+"/") {
			remote, ok = r, true
		}
	}
	if !ok {
		return "", "", false
	}
	return remote, remoteBranch[len(remote)+1:], true
}
