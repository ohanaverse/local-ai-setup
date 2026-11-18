// Package modelclient is the session side of the wt-model split: it finds the
// model server and talks to it.
package modelclient

import (
	"errors"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tokenfile"
)

// Endpoint is where the model server is and the token that opens it.
type Endpoint struct {
	URL, Token string
}

// Origin is the URL without trailing slashes.
func (e Endpoint) Origin() string { return strings.TrimRight(e.URL, "/") }

// Gateway returns the origin and token a streaming gateway client needs.
func (e Endpoint) Gateway() (origin, token string) { return e.Origin(), e.Token }

// NotConfiguredError says the model server url or token is missing. Its three
// leads are kept byte for byte (llmbench's wt_bridge hint and scripts key on
// them); the tail names the settings and wt config's mode choice.
type NotConfiguredError struct {
	MissingURL, MissingToken bool
}

func (e *NotConfiguredError) Error() string {
	lead := "no model server is configured"
	switch {
	case e.MissingURL && e.MissingToken:
		lead += "; [litellm] is no longer used by wt"
	case e.MissingURL:
		lead = "the model server url is not configured"
	case e.MissingToken:
		lead = "the model server token is not configured"
	}
	return lead + " — set url and token_file under [model-server] in config.toml (or WT_MODEL_URL and WT_MODEL_TOKEN_FILE), or choose the mode in wt config (Model server tab): native, local or remote"
}

// Still appends what did not happen (or did) to err, keeping err as the cause.
func Still(err error, what string) error {
	return &stillError{err: err, what: what}
}

type stillError struct {
	err  error
	what string
}

func (e *stillError) Error() string { return e.err.Error() + "; " + e.what }
func (e *stillError) Unwrap() error { return e.err }

// SessionTokenError words a token file failure for a session: it names the
// config key to check, not the server's flag (a loose file keeps its chmod
// sentence). Other errors pass through.
func SessionTokenError(err error) error {
	return sessionTokenError(err, false)
}

// sessionTokenError is SessionTokenError with the source of the failing path:
// fromEnv names WT_MODEL_TOKEN_FILE as the thing to fix instead of config.
func sessionTokenError(err error, fromEnv bool) error {
	var te *tokenfile.Error
	if !errors.As(err, &te) {
		return err
	}
	if fromEnv {
		return Still(err, "check the file WT_MODEL_TOKEN_FILE names")
	}
	return Still(err, "check token_file under [model-server] in config.toml")
}

// ResolveEndpoint decides the endpoint. The environment wins over config,
// highest first: WT_MODEL_URL, then WT_MODEL_TOKEN_FILE, then WT_MODEL_TOKEN
// as a literal. tokenFile must already have ~ expanded.
func ResolveEndpoint(url, tokenFile string, env func(string) string) (Endpoint, error) {
	if v := env("WT_MODEL_URL"); v != "" {
		url = v
	}
	var tok string
	fromEnv := false
	switch {
	case env("WT_MODEL_TOKEN_FILE") != "":
		tokenFile = env("WT_MODEL_TOKEN_FILE")
		fromEnv = true
		fallthrough
	case tokenFile != "" && env("WT_MODEL_TOKEN") == "":
		t, err := tokenfile.Load(tokenFile)
		if err != nil {
			return Endpoint{}, sessionTokenError(err, fromEnv)
		}
		tok = t
	case env("WT_MODEL_TOKEN") != "":
		tok = env("WT_MODEL_TOKEN")
	}
	if url == "" || tok == "" {
		return Endpoint{}, &NotConfiguredError{MissingURL: url == "", MissingToken: tok == ""}
	}
	return Endpoint{URL: url, Token: tok}, nil
}

// NoServerConfigured reports whether err says no model server is configured at
// all: neither the url nor the token is set (anywhere, wrapped or not). Since
// the mode exists this no longer chooses a native launch (only native mode
// does, IsNativeMode); it is the legacy half of NoServer, for callers that
// treat "nothing to ask" softly.
func NoServerConfigured(err error) bool {
	var nc *NotConfiguredError
	return errors.As(err, &nc) && nc.MissingURL && nc.MissingToken
}
