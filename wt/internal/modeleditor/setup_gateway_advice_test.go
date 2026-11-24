package modeleditor

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// TestGatewayRestartAdviceFollowsMode pins #693's review fix: x then s is
// advised only where those keys work (local mode).
func TestGatewayRestartAdviceFollowsMode(t *testing.T) {
	if got := gatewayRestartText(modelmode.ModeLocal); !strings.Contains(got, "x then s") {
		t.Errorf("local: %q lacks x then s", got)
	}
	for _, mode := range []modelmode.Mode{modelmode.ModeRemote, modelmode.ModeNative} {
		for name, got := range map[string]string{
			"status": gatewayRestartText(mode), "row": gatewayRestartAfter(mode), "prompt": gatewayRestartPrompt(mode),
		} {
			if strings.Contains(got, "x then s") || !strings.Contains(got, "launchctl") {
				t.Errorf("%s %s: %q must name launchctl, not x then s", mode, name, got)
			}
		}
	}
}
