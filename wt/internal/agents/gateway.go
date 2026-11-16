package agents

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// Gateway is the model-server gateway a session launches agents against:
// the one origin and the one token an agent ever sees. No provider origin and
// no provider key reaches a launch built from it.
type Gateway struct {
	Origin string // scheme://host:port, no wire-path suffix
	Token  string // the gateway token (not a provider key)
}

// origin is Origin without a trailing slash, the form every driver appends a
// wire path to.
func (g Gateway) origin() string { return strings.TrimRight(g.Origin, "/") }

// gatewayID is the id an agent sends to the gateway for m: its GatewayID, or
// its registry ID when the server gave none.
func gatewayID(m config.Model) string {
	if m.GatewayID != "" {
		return m.GatewayID
	}
	return m.ID
}

// GatewayRoute is the Route that reaches m through gw. A native model (and the
// empty model a command agent passes) gets the zero Route: it launches bare,
// and the gateway token is never handed to it.
func GatewayRoute(gw Gateway, m config.Model) config.Route {
	if m.Native || (m.ID == "" && m.ProviderID == "") {
		return config.Route{}
	}
	return config.Route{
		BaseOrigin: gw.origin(),
		APIKey:     gw.Token,
		ModelRef:   gatewayID(m),
		Display:    m.ModelName,
		ProviderID: m.ProviderID,
		Litellm:    true,
	}
}

// GatewaySyncer is an optional Driver capability: a pre-launch step that
// writes whatever the agent reads about the gateway (pi's models.json). models
// is the server's non-native catalog; target is the model about to launch.
type GatewaySyncer interface {
	SyncGateway(gw Gateway, models []config.Model, target config.Model) error
}

// BuildLaunchCmdGateway is BuildLaunchCmdInfo against the gateway: the same
// launch construction with GatewayRoute for the route and GatewaySyncer for
// the sync. models is the server's non-native models, for pi's file.
func BuildLaunchCmdGateway(agent string, m config.Model, worktreePath string, yolo bool, gw Gateway, models []config.Model, extraArgs []string) (*exec.Cmd, LaunchInfo, error) {
	d := ByName(agent)
	if d == nil {
		return nil, LaunchInfo{}, fmt.Errorf("unknown agent: %s", agent)
	}
	route := GatewayRoute(gw, m)
	if route.Litellm {
		if gw.Origin == "" || gw.Token == "" {
			return nil, LaunchInfo{}, fmt.Errorf("the model-server gateway needs both an origin and a token")
		}
		if s, ok := d.(GatewaySyncer); ok {
			if err := s.SyncGateway(gw, models, m); err != nil {
				return nil, LaunchInfo{}, err
			}
		}
	}
	return launchWithRoute(d, m, worktreePath, yolo, route, extraArgs)
}

// BuildSlotLaunchCmdGateway is BuildSlotLaunchCmd with gw in place of the
// [litellm] url and key.
func BuildSlotLaunchCmdGateway(sl profiles.SlotLaunch, worktreePath string, yolo bool, gw Gateway, extraArgs []string) (*exec.Cmd, error) {
	if err := CheckSlotPassthrough(sl.Agent, extraArgs); err != nil {
		return nil, err
	}
	if gw.origin() == "" || gw.Token == "" {
		return nil, fmt.Errorf("a slot profile needs the model-server gateway, but its origin or token is empty")
	}
	return slotLaunchCmd(sl, worktreePath, yolo, gw.origin(), gw.Token, extraArgs)
}
