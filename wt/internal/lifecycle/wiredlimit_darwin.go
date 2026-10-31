//go:build darwin

package lifecycle

import (
	"context"
	"time"
)

// realReadWiredLimit reads iogpu.wired_limit_mb and hw.memsize in one sysctl
// call (parseWiredLimit). It only reads: wt never sets the limit.
func realReadWiredLimit() (mb int64, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := runCommandStdout(ctx, "sysctl", "-n", "iogpu.wired_limit_mb", "hw.memsize")
	if err != nil {
		return 0, false, err
	}
	return parseWiredLimit(string(out))
}
