package agents

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

// nodeCAVar is the variable Node (claude, pi) reads extra trusted roots from;
// nodeCAMarker is wt's record that the value is its own, so a later launch
// that does not reach an https gateway can take it back off (decision 14).
const (
	nodeCAVar    = "NODE_EXTRA_CA_CERTS"
	nodeCAMarker = "WT_NODE_EXTRA_CA_CERTS"
)

// nodeCAAgents are the agents that run on Node and so read nodeCAVar.
var nodeCAAgents = map[string]bool{"claude": true, "pi": true}

// nodeCAEnv settles NODE_EXTRA_CA_CERTS for one launch (#597, decision 14).
// environ is the launch's assembled environment (inherited plus the
// driver's own), caFile the gateway's ca_file, already absolute
// (modelclient.ResolveEndpoint).
//
// For claude and pi over an https origin with caFile set: when environ lacks
// the variable (or holds wt's own marked value for another file), set names
// the variable and its marker, both caFile, and clear both names first. A
// value equal to caFile is left alone. A user's value naming another file is
// left too (Node takes one file): with a note in token mode, and in header
// mode an error unless that file holds every certificate in caFile, since
// the agent's first gateway request would otherwise fail opaquely.
//
// Every other launch (native, an http origin, no caFile, an agent not on
// Node) sets nothing and clears the variable and its marker when the value
// is wt's own (equal to the marker): a nested launch does not keep trusting
// the gateway's certificate on its way elsewhere. A user's value without the
// marker stays; a marker left beside a value that is not wt's is cleared.
func nodeCAEnv(agent, origin, caFile string, header bool, environ []string) (set, clear []string, note string, err error) {
	cur, mark := lastValue(environ, nodeCAVar), lastValue(environ, nodeCAMarker)
	wtOwned := cur != "" && cur == mark
	https := strings.HasPrefix(strings.ToLower(origin), "https://")
	if !nodeCAAgents[agent] || !https || caFile == "" {
		switch {
		case wtOwned:
			clear = []string{nodeCAVar, nodeCAMarker}
		case mark != "":
			clear = []string{nodeCAMarker}
		}
		return nil, clear, "", nil
	}
	switch {
	case cur == caFile:
		// Already this file: wt's from an outer launch, or the user's.
		return nil, nil, "", nil
	case cur == "" || wtOwned:
		return []string{nodeCAVar + "=" + caFile, nodeCAMarker + "=" + caFile}, []string{nodeCAVar, nodeCAMarker}, "", nil
	}
	if mark != "" {
		clear = []string{nodeCAMarker}
	}
	if holdsAll(cur, caFile) {
		return nil, clear, "", nil
	}
	shown := shownEnvPath(cur)
	if header {
		return nil, nil, "", fmt.Errorf("%s names %s, which does not hold the ca_file certificate: unset it or add the certificate to it", nodeCAVar, shown)
	}
	note = fmt.Sprintf("wt: note: %s names %s, which does not hold the ca_file certificate; wt leaves it, so %s may not trust the model server's certificate", nodeCAVar, shown, agent)
	return nil, clear, note, nil
}

// applyNodeCA is nodeTLSCheck, then nodeCAEnv applied to env: the cleared
// names removed, then the set entries appended; then noProxyEnv (the
// header-mode proxy leak guard). Notes go to stderr. Both launch paths
// (command, slotLaunchCmd) call it.
func applyNodeCA(agent string, r Route, header bool, env []string) ([]string, error) {
	tlsNote, err := nodeTLSCheck(agent, r.BaseOrigin, header, env)
	if err != nil {
		return nil, err
	}
	if tlsNote != "" {
		fmt.Fprintln(os.Stderr, tlsNote)
	}
	set, clear, note, err := nodeCAEnv(agent, r.BaseOrigin, r.CAFile, header, env)
	if err != nil {
		return nil, err
	}
	if note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	if len(clear) > 0 {
		env = filterEnv(env, clear)
	}
	return noProxyEnv(agent, r.BaseOrigin, header, append(env, set...)), nil
}

// lastValue is the value of the last entry naming key in env ("" when none):
// the one a child process sees.
func lastValue(env []string, key string) string {
	v := ""
	for _, e := range env {
		if k, val, ok := strings.Cut(e, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// holdsAll reports whether the PEM file at path holds every certificate in
// the PEM file at caFile; false when either cannot be read or caFile holds
// none.
func holdsAll(path, caFile string) bool {
	want, err := pemCerts(caFile)
	if err != nil || len(want) == 0 {
		return false
	}
	have, err := pemCerts(path)
	if err != nil {
		return false
	}
	for _, w := range want {
		found := false
		for _, h := range have {
			if bytes.Equal(w, h) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// pemCerts is the DER bytes of every CERTIFICATE block in the file at path,
// read within modelping.ReadCAFile's bounds (a regular file, 1 MiB at most;
// a FIFO never blocks).
func pemCerts(path string) ([][]byte, error) {
	data, err := modelping.ReadCAFile(path)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for rest := data; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return out, nil
		}
		if b.Type == "CERTIFICATE" {
			out = append(out, b.Bytes)
		}
	}
}

// shownEnvPath is an inherited path as a message may print it: a fixed
// stand-in for one holding a control character or too long to be a path.
func shownEnvPath(p string) string {
	if len(p) > 200 {
		return "<path too long>"
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "<unprintable path>"
		}
	}
	return p
}

// NodeTLSRejectVar turns off Node's certificate verification when set to 0
// (wt also reads false that way).
const NodeTLSRejectVar = "NODE_TLS_REJECT_UNAUTHORIZED"

// NodeTLSVerifyOff reports whether v, a NODE_TLS_REJECT_UNAUTHORIZED value,
// turns certificate verification off: 0 or false (any case, trimmed).
func NodeTLSVerifyOff(v string) bool {
	v = strings.TrimSpace(v)
	return v == "0" || strings.EqualFold(v, "false")
}

// nodeTLSCheck judges NODE_TLS_REJECT_UNAUTHORIZED in a launch's
// environment. Set to turn verification off, it lets anyone on the path
// answer for the gateway: a header-mode launch (the user's Claude login
// rides on it) refuses, naming the variable and never its value; a
// token-mode Node agent over an https gateway gets one note. It is never
// stripped silently.
func nodeTLSCheck(agent, origin string, header bool, environ []string) (string, error) {
	if !NodeTLSVerifyOff(lastValue(environ, NodeTLSRejectVar)) {
		return "", nil
	}
	if header {
		return "", fmt.Errorf("%s is set to turn off certificate checks, which a launch using your Claude subscription refuses: unset it", NodeTLSRejectVar)
	}
	if nodeCAAgents[agent] && strings.HasPrefix(strings.ToLower(origin), "https://") {
		return fmt.Sprintf("wt: note: %s turns off %s's certificate check of the model server's gateway", NodeTLSRejectVar, agent), nil
	}
	return "", nil
}
