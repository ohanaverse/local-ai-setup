package litellm

import "context"

// runShell runs a shell command; a package var so tests never execute the
// real restart command. Replaced by the real implementation in restart.go's
// full form (Task 3).
var runShell = func(ctx context.Context, cmd string) error { return nil }
