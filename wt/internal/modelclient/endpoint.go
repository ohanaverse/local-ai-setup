// Package modelclient is the session side of the wt-model split: it finds the
// model server and talks to it.
package modelclient

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlsfile"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tokenfile"
)

// Endpoint is where the model server is and the token that opens it. It
// lives in internal/modelping; this name is its modelclient alias.
type Endpoint = modelping.Endpoint

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
	return lead + " — set url and token_file under [model-server] in config.toml (or WT_MODEL_URL and WT_MODEL_TOKEN_FILE), or choose the mode in wt config (Mode tab): native, local or remote"
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
// as a literal. tokenFile must already have ~ expanded. The url it settles
// on is refused with *CredentialsInURLError when it holds an '@' (HasUserinfo),
// whichever source it came from, before a token file is read or any request
// is built.
//
// caFile is [model-server] ca_file (decision 14); WT_MODEL_CA_FILE wins over
// it. For an https url its leading ~ is expanded, it is made absolute (the
// agent runs in the worktree and must read the same file) and checked before
// any request: a relative path from config.toml (modelping.RelativeConfigCAFile), a
// file that is not regular, over 1 MiB, unreadable, holding no PEM
// certificate or holding a private key is a *modelping.CAFileError naming
// the path, never its bytes; a CA
// certificate without name constraints resolves with one note. For any other
// url it is neither read nor used, with one note.
func ResolveEndpoint(url, tokenFile, caFile string, env func(string) string) (Endpoint, error) {
	if v := env("WT_MODEL_URL"); v != "" {
		url = v
	}
	if HasUserinfo(url) {
		return Endpoint{}, &CredentialsInURLError{}
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
	ep := Endpoint{URL: url, Token: tok}
	caFromEnv := false
	if v := env("WT_MODEL_CA_FILE"); v != "" {
		caFile, caFromEnv = v, true
	}
	if caFile == "" {
		return ep, nil
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(url)), "https://") {
		ep.Notes = append(ep.Notes, "ca_file is ignored for an http url")
		return ep, nil
	}
	if !caFromEnv && modelping.RelativeConfigCAFile(caFile) {
		return Endpoint{}, &modelping.CAFileError{Path: caFile, Reason: modelping.RelativeCAReason}
	}
	path, notes, err := checkCAFile(caFile)
	if err != nil {
		return Endpoint{}, err
	}
	ep.CAFile, ep.Notes = path, append(ep.Notes, notes...)
	return ep, nil
}

// checkCAFile expands a leading ~ in p, makes it absolute and checks the
// file: it must be readable, hold at least one PEM certificate and no
// private-key block. notes holds one line when a certificate in it is a CA
// without name constraints (it can vouch for any host).
func checkCAFile(p string) (string, []string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", nil, &modelping.CAFileError{Path: p, Reason: "cannot be read: no home directory to expand ~ against"}
		}
		p = filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", nil, &modelping.CAFileError{Path: p, Reason: "cannot be read: " + err.Error()}
	}
	data, err := modelping.ReadCAFile(abs)
	if err != nil {
		return "", nil, err
	}
	if tlsfile.HasPrivateKey(data) {
		return "", nil, &modelping.CAFileError{Path: abs, Reason: modelping.CAKeyReason}
	}
	var certs []*x509.Certificate
	for rest := data; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		if c, err := x509.ParseCertificate(b.Bytes); err == nil {
			certs = append(certs, c)
		}
	}
	if len(certs) == 0 {
		return "", nil, &modelping.CAFileError{Path: abs, Reason: "holds no PEM certificate"}
	}
	for _, c := range certs {
		if c.IsCA && !nameConstrained(c) {
			return abs, []string{"ca_file " + abs + " holds a CA certificate without name constraints: it can vouch for any host, Anthropic's included"}, nil
		}
	}
	return abs, nil, nil
}

// nameConstrained reports whether c carries any name constraint.
func nameConstrained(c *x509.Certificate) bool {
	return len(c.PermittedDNSDomains)+len(c.ExcludedDNSDomains)+len(c.PermittedIPRanges)+len(c.ExcludedIPRanges)+
		len(c.PermittedEmailAddresses)+len(c.ExcludedEmailAddresses)+len(c.PermittedURIDomains)+len(c.ExcludedURIDomains) > 0
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
