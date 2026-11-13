package rootclaim

import "fmt"

// Drift is the one drift line for root s against defaultBranch: how
// far the root has left its default branch, worded as the cleanup root row's
// reason detail and the launch warning's text both render it. The shapes, in
// precedence order:
//
//	""                        no default branch is known (origin/HEAD unset):
//	                          no drift is invented without one, whatever the
//	                          branch — a detached HEAD included
//	"root is detached"        HEAD is detached (s.Branch is "", the Site
//	                          spelling)
//	""                        the root sits on the default branch
//	"root is on <b>, not <d>" anything else
//
// It computes from s.Branch and defaultBranch alone and reads no git: the
// callers (launchwarn.ForDir's root read, cleanup's gather and rescan) read
// live state and pass it in, so every list and every rescan re-renders what
// is true now. s.Branch is Site's spelling — "" on a detached HEAD — never
// Root's "(detached)" display sentinel, which a real branch may be named.
func Drift(s Site, defaultBranch string) string {
	switch {
	case defaultBranch == "":
		return ""
	case s.Branch == "":
		return "root is detached"
	case s.Branch == defaultBranch:
		return ""
	}
	return fmt.Sprintf("root is on %s, not %s", s.Branch, defaultBranch)
}
