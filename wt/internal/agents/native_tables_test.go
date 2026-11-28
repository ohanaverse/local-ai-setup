package agents

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestNativeTablesMatchTheDrivers pins config.NativeCapableAgents and
// config.NativeOnlyAgents (leaf tables, because internal/catalog may not import
// this package) to the registered drivers they describe: every native-capable
// name is a registered model-driven driver; the drivers outside the set are
// exactly pi and opencode (so a new driver forces a decision about its native
// provider); a native-only agent's Build ignores the model, so offering it
// only its own native row loses nothing; and claude, codex and copilot's
// native rows never carry the gateway origin or token, which is what makes
// offering an agent its own native row safe.
func TestNativeTablesMatchTheDrivers(t *testing.T) {
	// (1) every native-capable name is a registered, non-command driver.
	for _, n := range config.NativeCapableAgents {
		if ByName(n) == nil {
			t.Errorf("NativeCapableAgents names %q, which is not a registered driver", n)
		} else if IsCommand(n) {
			t.Errorf("NativeCapableAgents names %q, which is a command, not an agent", n)
		}
	}

	// (2) the registered agents outside the set are exactly pi and opencode.
	var outside []string
	for _, n := range Names() {
		if IsCommand(n) || slices.Contains(config.NativeCapableAgents, n) {
			continue
		}
		outside = append(outside, n)
	}
	slices.Sort(outside)
	if want := []string{"opencode", "pi"}; !slices.Equal(outside, want) {
		t.Errorf("registered agents with no native provider = %v, want %v: whoever adds a driver must decide whether it has a native provider (add it to config.NativeCapableAgents and to the seeded natives, or extend this list)", outside, want)
	}

	// (3) a native-only agent's Build does not depend on the model or route.
	for _, n := range config.NativeOnlyAgents {
		d := ByName(n)
		if d == nil {
			t.Errorf("NativeOnlyAgents names %q, which is not a registered driver", n)
			continue
		}
		native := d.Build(nativeModel(n), false, config.Route{})
		cloud := gwCloudModel()
		viaGateway := d.Build(cloud, false, GatewayRoute(testGW(), cloud))
		if !reflect.DeepEqual(native, viaGateway) {
			t.Errorf("%s: Build depends on the model (native %+v, gateway %+v), so it is not native-only", n, native, viaGateway)
		}
	}

	// (4) a native row of claude, codex or copilot carries no gateway fact.
	for _, n := range []string{"claude", "codex", "copilot"} {
		fakeBin(t, n)
		cmd, _, err := BuildLaunchCmdGateway(n, nativeModel(n), t.TempDir(), false, testGW(), nil, nil)
		if err != nil {
			t.Errorf("%s: native build: %v", n, err)
			continue
		}
		all := strings.Join(append(slices.Clone(cmd.Args), cmd.Env...), "\n")
		for _, secret := range []string{testGWToken, testGWOrigin} {
			if strings.Contains(all, secret) {
				t.Errorf("%s: native launch carries %q", n, secret)
			}
		}
	}
}
