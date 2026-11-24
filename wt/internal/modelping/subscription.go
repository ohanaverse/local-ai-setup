package modelping

import "fmt"

// SubscriptionOffReason is decision 2's one message for a model server whose
// ping lacks subscription_passthrough: off and older look the same. rawURL is
// the server's url; the message names only its host:port (Addr), never the
// url. The launch guards (cmd/wt-session) and the model picker (internal/tui)
// both say it, so a Claude subscription row reads the same everywhere.
func SubscriptionOffReason(rawURL string) string {
	return fmt.Sprintf("the model server at %s does not accept a Claude login: its subscription passthrough is off, or it is older than this wt (on the host: make install, set [gateway] subscription_passthrough = true in ~/.config/wt-model/config.toml, restart wt-model serve)", Addr(rawURL))
}
