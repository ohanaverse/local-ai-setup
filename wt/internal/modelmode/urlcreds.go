package modelmode

import (
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// The url words an observation or a plan may hold: no parsing, no partial
// echo. A model-server url has no legitimate '@', so any url holding one is
// refused, and nothing derived from it (no host, no user, no piece) is ever
// shown: the whole url is replaced by ShownURL's result (urlrule.Hidden),
// and it is never pinged (the check reads as not made, with NotCheckedAtURL);
// a check message that may still quote one is replaced by CheckHidden, as
// defence in depth. The rule itself is urlrule's; these are the words built
// on it for the state a plan and an observation carry.

// NotCheckedAtURL is ServerState.Message for a url holding an '@': Observe
// never pings one, so the check is not made, and the words hold none of it.
const NotCheckedAtURL = "not checked: the url contains an @; retype it without credentials"

// CheckHidden replaces a check message that may quote a url holding an '@'.
const CheckHidden = "the check failed; the url contains an @ so the error text is hidden"

// ShownURL is the form of a url that State, a plan or a message may hold:
// the url exactly as typed when it has no '@', else urlrule.Hidden, whole.
func ShownURL(raw string) string { return urlrule.Shown(raw) }

// RedactCheck guards a failed check's message, which may quote the address
// it dialled (the ping's UnreachableError does). urls are every url the
// observer knows (the entered one, the stored one, WT_MODEL_URL). Fail
// closed: the whole message becomes CheckHidden when it holds an '@', or
// when any of those urls holds an '@' and the message holds any run of 3 or
// more bytes copied from that url. A false hit only hides an error text; a
// miss would show a password. Text that meets neither comes back unchanged.
func RedactCheck(msg string, urls ...string) string {
	if msg == "" {
		return msg
	}
	if urlrule.HasUserinfo(msg) {
		return CheckHidden
	}
	for _, u := range urls {
		if !urlrule.HasUserinfo(u) {
			continue
		}
		for i := 0; i+3 <= len(u); i++ {
			if strings.Contains(msg, u[i:i+3]) {
				return CheckHidden
			}
		}
	}
	return msg
}
