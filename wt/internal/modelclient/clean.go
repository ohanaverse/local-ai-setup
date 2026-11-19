package modelclient

import "github.com/ohanaverse/local-ai-setup/wt/internal/modelping"

// clean is this package's name for modelping.Clean: the server is a remote
// party (style guide M11), so every string it sends passes through here
// before wt shows it. It mirrors cleanup.Flatten, which this package cannot
// import (cleanup pulls in git); a test in modelserver keeps the two equal.
func clean(s string) string { return modelping.Clean(s) }

// CleanForTest exposes clean to the test that compares it with cleanup.Flatten.
func CleanForTest(s string) string { return clean(s) }

func cleanAll(ss []string) []string {
	for i, s := range ss {
		ss[i] = clean(s)
	}
	return ss
}
