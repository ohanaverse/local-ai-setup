package modelsetup

import (
	"context"
	"strings"
	"testing"
)

// TestDefaultEnvRefusesLaunchdAndListenerLookupsUnderTest proves the real
// launchctl, lsof and ps seams fail closed in a test binary, so no test can
// load, unload or inspect the developer's real services.
func TestDefaultEnvRefusesLaunchdAndListenerLookupsUnderTest(t *testing.T) {
	env := DefaultEnv()
	ctx := context.Background()
	checks := map[string]error{
		"Bootstrap": env.Launchd.Bootstrap(ctx, "/nonexistent.plist"),
		"Bootout":   env.Launchd.Bootout(ctx, AgentLabel),
		"Kickstart": env.Launchd.Kickstart(ctx, AgentLabel, true),
	}
	_, err := env.Launchd.Print(ctx, AgentLabel)
	checks["Print"] = err
	_, _, err = env.Net.ListenerPID(ctx, 8787)
	checks["ListenerPID"] = err
	_, err = env.Net.ProcessStart(ctx, 1)
	checks["ProcessStart"] = err
	for name, err := range checks {
		if err == nil || !strings.Contains(err.Error(), "test binary") {
			t.Errorf("%s: err = %v, want one mentioning \"test binary\"", name, err)
		}
	}
}
