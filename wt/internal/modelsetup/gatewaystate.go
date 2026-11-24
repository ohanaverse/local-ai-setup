package modelsetup

import "github.com/ohanaverse/local-ai-setup/wt/internal/config"

// GatewayState is the wt-model home's [gateway] subscription switch as the
// Setup tab shows it (#693). serve reads the switch once, at start, so this
// is what the next start will use, not necessarily what the running serve
// does.
type GatewayState struct {
	// SubscriptionPassthrough is the switch (off when absent).
	SubscriptionPassthrough bool
	// Err is why the table cannot be read, or its one warning (a value of the
	// wrong type reads as off); value-free either way. "" when it reads cleanly.
	Err string
}

// realGateway is Env.Gateway on the real machine.
func realGateway() GatewayState {
	gw, warnings, err := config.GatewaySettings()
	st := GatewayState{SubscriptionPassthrough: gw.SubscriptionPassthrough}
	switch {
	case err != nil:
		st.Err = err.Error()
	case len(warnings) > 0:
		st.Err = warnings[0]
	}
	return st
}
