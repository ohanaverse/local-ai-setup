package modelsetup

import "strings"

// The one rule for a url that may carry credentials, shared by the plan's
// refusal, Apply's config-step backstop and every place a url is shown or
// stored in State: no parsing, no partial echo. A model-server url has no
// legitimate '@', so any url holding one is refused, and nothing derived from
// it (no host, no user, no piece) is ever shown: the whole url is replaced
// by hiddenURL, and it is never pinged (the check reads as not made, with
// notCheckedAtURL); a check message that may still quote one is replaced by
// checkHidden, as defence in depth.
// Finding the credential span in an arbitrary string was tried twice and
// bypassed twice; this rule has nothing to find.

// hiddenURL stands in for a url holding an '@' wherever one would be shown
// or held in State. It holds an '@' itself, so it is refused like the url it
// stands for and can never be written into config.toml.
const hiddenURL = "(hidden: the url contains an @)"

// notCheckedAtURL is ServerState.Message for a url holding an '@': Observe
// never pings one, so the check is not made, and the words hold none of it.
const notCheckedAtURL = "not checked: the url contains an @; retype it without credentials"

// checkHidden replaces a check message that may quote a url holding an '@'.
const checkHidden = "the check failed; the url contains an @ so the error text is hidden"

// hasUserinfo reports whether a typed url holds an ASCII '@' anywhere: every
// such url is read as carrying a user:password and refused.
func hasUserinfo(raw string) bool {
	return strings.Contains(raw, "@")
}

// shownURL is the form of a url that State, a plan or a message may hold:
// the url exactly as typed when it has no '@', else hiddenURL, whole.
func shownURL(raw string) string {
	if hasUserinfo(raw) {
		return hiddenURL
	}
	return raw
}

// redactCheck guards a failed check's message, which may quote the address
// it dialled (modelclient's UnreachableError does). urls are every url the
// observer knows (the entered one, the stored one, WT_MODEL_URL). Fail
// closed: the whole message becomes checkHidden when it holds an '@', or
// when any of those urls holds an '@' and the message holds any run of 3 or
// more bytes copied from that url. A false hit only hides an error text; a
// miss would show a password. Text that meets neither comes back unchanged.
func redactCheck(msg string, urls ...string) string {
	if msg == "" {
		return msg
	}
	if hasUserinfo(msg) {
		return checkHidden
	}
	for _, u := range urls {
		if !hasUserinfo(u) {
			continue
		}
		for i := 0; i+3 <= len(u); i++ {
			if strings.Contains(msg, u[i:i+3]) {
				return checkHidden
			}
		}
	}
	return msg
}
