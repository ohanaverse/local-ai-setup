package agents

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// NativeModel is the model of a native launch: wt-session started an agent
// with no model server configured, so the agent's own configuration decides
// the model. It is the zero-ID, Native sentinel every driver already accepts
// (agy's only kind), and GatewayRoute maps it to the zero Route, so neither a
// gateway origin nor a token ever reaches the agent. It has no ID, so no
// rotation, usage or survey record is made for it and the summary line names
// no model.
func NativeModel() config.Model {
	return config.Model{Native: true, ModelName: "native"}
}

// NativeNote is the one stderr line a native launch prints, so the fallback is
// never silent: what happened, whose configuration decides the model, and the
// key that turns wt-model on. It is plain information, so it is a bare
// `wt: <sentence>` (style guide M4), not a `wt: note:`.
func NativeNote(agent string) string {
	return fmt.Sprintf("wt: no model server is configured — launching %s natively, with its own configuration; to choose models through wt-model, set url and token_file under [model-server] in config.toml", agent)
}

// isNativeLaunch reports whether m is the model of a native launch (see
// NativeModel): the zero-ID sentinel. Unlike a registered native model
// (claude/native), which clears the gateway variables so the subscription
// wins, a native launch leaves the user's own environment alone: their
// ANTHROPIC_* or COPILOT_* variables are the "own configuration" the note
// promises.
func isNativeLaunch(m config.Model) bool {
	return m.Native && m.ID == "" && m.ProviderID == ""
}
