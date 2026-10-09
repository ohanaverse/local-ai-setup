package ollamacheck

// StubListForTest replaces what Check and Available learn from `ollama list`
// with fn, and returns the function that puts the previous answer back
// (`t.Cleanup(ollamacheck.StubListForTest(fn))`). fn gets the origin the
// command would have been pinned to and returns the names listed there,
// whether an ollama command exists at all, and the command's error.
// Production code never calls it; it is exported, like internal/localmodels'
// ...ForTest helpers, because the code that reaches Check lives in other
// packages (cmd/wt, internal/tui), whose tests must not run the developer's
// ollama.
func StubListForTest(fn func(origin string) (names []string, installed bool, err error)) (restore func()) {
	old := list
	list = fn
	return func() { list = old }
}
