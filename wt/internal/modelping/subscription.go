package modelping

import "fmt"

// SubscriptionOffReason is decision 2's one message for a model server whose
// ping lacks subscription_passthrough: off, older and a hazard the server
// turned the mode off over while serving (#697) all look the same. rawURL is the server's url; the message names only its host:port (Addr),
// never the url. The launch guards (cmd/wt-session) and the model picker
// (internal/tui) both say it, so a Claude subscription row reads the same
// everywhere.
func SubscriptionOffReason(rawURL string) string {
	return fmt.Sprintf("the model server at %s does not accept a Claude login: its subscription passthrough is off, or it is older than this wt (on the host: make install, set [gateway] subscription_passthrough = true in the host's wt-model config.toml (by default ~/.config/wt-model/config.toml), restart wt-model serve; or a hazard turned it off while serving, which the wt-model serve log names)", Addr(rawURL))
}

// SubscriptionOffShort is the one-line form of SubscriptionOffReason for a
// screen that has one line for it: the model picker's mode line, where the
// whole message is cut before its fix at 80 columns (#699). The picker reads
// "<id> uses your Claude subscription; " + this, and Enter still shows the
// whole message. It names the condition only: the fix is the whole message's
// job.
const SubscriptionOffShort = "passthrough off"
