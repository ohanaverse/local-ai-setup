package modelsetup

// The url rule itself lives in internal/urlrule (the '@' anywhere rule) and
// its observation words in internal/modelmode (ShownURL, RedactCheck,
// NotCheckedAtURL, CheckHidden), shared with the session side. This file
// keeps only the LiteLLM pair's own words: the pair is a different setting
// from the model-server url, under the same rule (#640).

// notCheckedPairAtURL is the Detail of every LiteLLM check that reads the
// wt-model pair url (reachable, database, key, routes, port) when that url
// holds an '@': CheckLiteLLM never requests one, and the words hold none of
// it. The pair is a different setting from the model-server url, under the
// same rule (#640).
const notCheckedPairAtURL = "not checked: the pair url contains an @"

// pairErrHidden replaces the pair's read error when its text holds an '@'
// (a TOML parse error may quote the line it stopped on, url and all).
const pairErrHidden = "wt-model's LiteLLM pair could not be read; the error text contains an @ so it is hidden"
