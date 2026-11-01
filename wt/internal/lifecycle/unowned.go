package lifecycle

import (
	"context"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// warnUnowned prints, for a family stopped by signalling its verified pid
// (an empty Stop, sushi), that the server already serving t is not one wt
// started. Such a server's flags (context size, max tokens) are invisible once
// it runs, so wt cannot know it fits a long prompt; the start is still a
// no-op and nothing is signalled, the line only tells the user to stop it
// themselves. A server wt verified as its own (loadingServer names a pid), and
// every family with a stop command, print nothing.
func (e *env) warnUnowned(ctx context.Context, cfg *config.Config, family string, t Target) {
	spec, ok := processSpecFor(family)
	if !ok || len(spec.Stop) != 0 {
		return
	}
	if _, l := e.loadingServer(cfg, family); l.PID != 0 {
		return
	}
	routePrintf(ctx, "wt: %s is already serving %s on port %d but wt did not start it; wt cannot see its flags (context size, max tokens), so a long prompt may be refused or truncated; stop it yourself to have wt start it with its own flags\n",
		spec.Binary, t.ModelName, serverPort(cfg, family))
}
