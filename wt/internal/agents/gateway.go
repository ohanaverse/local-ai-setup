package agents

import (
	"errors"
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

// ErrGatewayUnconfigured is what errors.Is finds in every empty-gateway
// refusal (no origin or no token); the message of each stays its own text.
var ErrGatewayUnconfigured = errors.New("the model-server gateway is not configured")

// gatewayUnconfigured is an error that reads msg and matches
// ErrGatewayUnconfigured.
type gatewayUnconfiguredError struct{ msg string }

func gatewayUnconfigured(msg string) error { return &gatewayUnconfiguredError{msg} }

func (e *gatewayUnconfiguredError) Error() string { return e.msg }

func (e *gatewayUnconfiguredError) Is(target error) bool { return target == ErrGatewayUnconfigured }

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

// BuildLaunchCmdGateway is the one launch constructor (the direct-route
// builders were deleted with Task 19): GatewayRoute for the route and
// GatewaySyncer for the sync. models is the server's non-native models, for
// pi's file.
//
// extraArgs are user-supplied args after `--`. For regular agents they are
// appended to the agent's command. For agents implementing ArgSetter (e.g.
// shell), the args are passed via SetArgs and not appended again.
//
// It never resumes a session. wt used to look up an agent's newest session
// for the worktree and add the agent's resume flag itself; that appended a
// one-shot run to whatever conversation was newest (#204), broke a launch
// whose resumed session had stored another model (#198), and doubled the flag
// when the user passed their own. Sessions are the agent's to manage: a user
// who wants one continued says so in extraArgs, with the agent's own flags
// (`-- --continue`, `-- --resume <id>`, `-- --session <id>`).
func BuildLaunchCmdGateway(agent string, m config.Model, worktreePath string, yolo bool, gw Gateway, models []config.Model, extraArgs []string) (*exec.Cmd, LaunchInfo, error) {
	d := ByName(agent)
	if d == nil {
		return nil, LaunchInfo{}, fmt.Errorf("unknown agent: %s", agent)
	}
	route := GatewayRoute(gw, m)
	if route.Litellm {
		if gw.origin() == "" || gw.Token == "" {
			return nil, LaunchInfo{}, gatewayUnconfigured("the model-server gateway needs both an origin and a token")
		}
		if s, ok := d.(GatewaySyncer); ok {
			if err := s.SyncGateway(gw, models, m); err != nil {
				return nil, LaunchInfo{}, err
			}
		}
	}
	return launchWithRoute(d, m, worktreePath, yolo, route, extraArgs)
}

// BuildSlotLaunchCmdGateway builds a slot profile's launch against gw, the
// only proxy a slot profile uses.
func BuildSlotLaunchCmdGateway(sl profiles.SlotLaunch, worktreePath string, yolo bool, gw Gateway, extraArgs []string) (*exec.Cmd, error) {
	if err := CheckSlotPassthrough(sl.Agent, extraArgs); err != nil {
		return nil, err
	}
	if gw.origin() == "" || gw.Token == "" {
		return nil, gatewayUnconfigured("a slot profile needs the model-server gateway, but its origin or token is empty")
	}
	return slotLaunchCmd(sl, worktreePath, yolo, gw.origin(), gw.Token, extraArgs)
}
