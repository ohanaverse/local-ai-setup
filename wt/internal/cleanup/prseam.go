package cleanup

// StubPRListForTest replaces the `gh pr list --head <branch>` call with fn and
// returns the function that puts the previous one back
// (`t.Cleanup(cleanup.StubPRListForTest(fn))`). Production code never calls
// it; it is exported because cmd/wt's tests reach the lookup through
// `wt cleanup`. It mirrors worktree.StubPRListForTest.
func StubPRListForTest(fn func(dir, branch string) ([]byte, error)) (restore func()) {
	old := ghPRList
	ghPRList = fn
	return func() { ghPRList = old }
}

// StubPRBaseListForTest replaces the `gh pr list --base <branch>` call with fn
// and returns the function that puts the previous one back. Production code
// never calls it.
func StubPRBaseListForTest(fn func(dir, base string) ([]byte, error)) (restore func()) {
	old := ghPRBaseList
	ghPRBaseList = fn
	return func() { ghPRBaseList = old }
}
