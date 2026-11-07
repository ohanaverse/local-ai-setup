package rescue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout bounds one git call.
const gitTimeout = 2 * time.Minute

// sgit runs git in one directory. Every call turns optional locks off and
// the fsmonitor daemon off: `git status` or `git diff` would otherwise be
// free to rewrite the index's stat cache, which is a change to a source
// that must stay as found.
type sgit struct{ dir string }

// out runs `git -C dir -c core.fsmonitor=false args...` with stdin, returning
// stdout. A non-zero exit is an error carrying stderr.
func (g sgit) out(stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	full := append([]string{"-C", g.dir, "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

func (g sgit) text(args ...string) (string, error) {
	b, err := g.out(nil, args...)
	return strings.TrimSpace(string(b)), err
}
