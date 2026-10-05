package litellm

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// defaultProxyPlist is the LaunchAgent the fallback restart command
// kickstarts (fallbackRestartCmd): the process whose environment LiteLLM's
// `os.environ/…` config references are resolved in.
const defaultProxyPlist = "~/Library/LaunchAgents/local.litellm.proxy.plist"

// ProxyEnv answers "is this variable set for the LiteLLM proxy?", and says
// where it looked.
//
// The question is the proxy's, not wt's (#211). LiteLLM resolves an
// `os.environ/VAR` value in config.yaml in its own process, and under launchd
// that process gets the LaunchAgent plist's EnvironmentVariables — not the
// shell wt was typed in. Asking wt's environment is wrong in both directions:
// a variable only the plist has would be reported unset, and one only the
// shell has would hide the very failure the question is asked to catch.
type ProxyEnv struct {
	// Source names what was consulted, for a warning to quote.
	Source string
	vars   map[string]string
	// ambient is set when the plist could not answer and wt's own
	// environment stands in for it.
	ambient bool
}

// IsSet reports whether name is set for the proxy. A variable set to the
// empty string is set: LiteLLM reads it as "", not as None.
func (e ProxyEnv) IsSet(name string) bool {
	if e.ambient {
		_, ok := os.LookupEnv(name)
		return ok
	}
	_, ok := e.vars[name]
	return ok
}

// ProxyPlistPath is the proxy's LaunchAgent plist: WT_LITELLM_PLIST, else the
// one the default restart command kickstarts.
func ProxyPlistPath() string {
	p := os.Getenv("WT_LITELLM_PLIST")
	if p == "" {
		p = defaultProxyPlist
	}
	if len(p) >= 2 && p[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	return p
}

// LoadProxyEnv reads the environment the proxy runs with: the LaunchAgent
// plist's EnvironmentVariables. A readable plist without that key is an
// answer — nothing is set. wt's own environment stands in only when the plist
// cannot answer: a restart command is configured (WT_LITELLM_RESTART_CMD or
// its legacy alias), so something other than that LaunchAgent starts the
// proxy; or the plist is missing, binary or malformed. Source records which.
//
// Not consulted: variables given to launchd itself (`launchctl setenv`),
// which a LaunchAgent also inherits.
func LoadProxyEnv() ProxyEnv {
	ambient := ProxyEnv{Source: "wt's own environment", ambient: true}
	if RestartCommand() != fallbackRestartCmd {
		return ambient
	}
	path := ProxyPlistPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return ambient
	}
	vars, err := plistEnvironment(data)
	if err != nil {
		return ambient
	}
	return ProxyEnv{Source: "the proxy LaunchAgent's EnvironmentVariables (" + path + ")", vars: vars}
}

// plistEnvironment returns the top-level EnvironmentVariables dict of an XML
// property list (empty when the key is absent). A binary plist, or XML that
// does not parse as <plist><dict>…, is an error. Only string values are kept;
// launchd accepts nothing else there.
func plistEnvironment(data []byte) (map[string]string, error) {
	if bytes.HasPrefix(data, []byte("bplist")) {
		return nil, errors.New("binary property list")
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	// Find the root <dict>.
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("no root dict: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			break
		}
	}
	vars := map[string]string{}
	found := false
	err := eachDictEntry(dec, func(key string, value xml.StartElement) error {
		if key != "EnvironmentVariables" || value.Name.Local != "dict" || found {
			return dec.Skip()
		}
		found = true
		return eachDictEntry(dec, func(name string, v xml.StartElement) error {
			if v.Name.Local != "string" {
				return dec.Skip()
			}
			var s string
			if err := dec.DecodeElement(&s, &v); err != nil {
				return err
			}
			vars[name] = s
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return vars, nil
}

// eachDictEntry walks the <key>/value pairs of the <dict> whose start tag was
// just read, up to and including its end tag. fn receives each key and its
// value's start tag and must consume that value (decode it or dec.Skip()).
func eachDictEntry(dec *xml.Decoder, fn func(key string, value xml.StartElement) error) error {
	key, haveKey := "", false
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		switch el := tok.(type) {
		case xml.EndElement:
			return nil
		case xml.StartElement:
			if el.Name.Local == "key" {
				if err := dec.DecodeElement(&key, &el); err != nil {
					return err
				}
				haveKey = true
				continue
			}
			if !haveKey {
				return fmt.Errorf("value <%s> with no key", el.Name.Local)
			}
			haveKey = false
			if err := fn(key, el); err != nil {
				return err
			}
		}
	}
}
