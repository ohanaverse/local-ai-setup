package modelsetup

import (
	"bytes"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// GenerateKey returns "sk-" plus 64 hex characters from 32 bytes drawn only
// from rand. A reader error is returned and no partial key is.
func GenerateKey(rand func([]byte) error) (string, error) {
	b := make([]byte, 32)
	if err := rand(b); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return "sk-" + hex.EncodeToString(b), nil
}

// GenerateSaltKey returns 64 raw hex characters (guide 14: `openssl rand -hex
// 32`, no sk- prefix) from 32 bytes drawn only from rand; no value on error.
func GenerateSaltKey(rand func([]byte) error) (string, error) {
	b := make([]byte, 32)
	if err := rand(b); err != nil {
		return "", fmt.Errorf("generate salt key: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// LiteLLMSpec is everything the minimal LiteLLM LaunchAgent plist is built
// from. MasterKey and SaltKey are secrets: String and GoString redact them.
// Hold a spec only in EXPORTED fields or by pointer: fmt cannot call
// String/GoString on an unexported field, so a struct with an unexported
// LiteLLMSpec field prints both keys raw.
type LiteLLMSpec struct {
	Binary, ConfigPath, Path, Home, MasterKey, SaltKey string
	Port                                               int
}

// String redacts the keys so an accidental %v or %+v never prints them.
func (s LiteLLMSpec) String() string {
	return fmt.Sprintf("LiteLLMSpec{Binary:%q ConfigPath:%q Home:%q Port:%d MasterKey:<redacted> SaltKey:<redacted>}",
		s.Binary, s.ConfigPath, s.Home, s.Port)
}

// GoString makes %#v redact the same way.
func (s LiteLLMSpec) GoString() string { return s.String() }

// RenderLiteLLMPlist renders the minimal LiteLLM LaunchAgent deterministically.
// The keys appear only as values of their EnvironmentVariables entries.
//
// It is deliberately smaller than the plist in docs/guides/14-litellm-install-
// and-configuration.md: no DATABASE_URL, UI_*, provider keys or MAX_* limits
// (wt does not know them, and a proxy without a database still starts for
// routing), no SoftResourceLimits.NumberOfFiles (a tuning the user can add),
// and the environment order is PATH, master, salt rather than the guide's.
// wt creates this only when no LiteLLM plist exists; it never edits the rest.
func RenderLiteLLMPlist(s LiteLLMSpec) []byte {
	var b strings.Builder
	str := func(indent, v string) { b.WriteString(indent + "<string>" + xmlEsc(v) + "</string>\n") }
	key := func(indent, k string) { b.WriteString(indent + "<key>" + k + "</key>\n") }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<!-- " + agentMarker + " -->\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	key("\t", "Label")
	str("\t", litellm.ProxyLabel)
	key("\t", "ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, a := range []string{s.Binary, "--config", s.ConfigPath, "--port", strconv.Itoa(s.Port)} {
		str("\t\t", a)
	}
	b.WriteString("\t</array>\n")
	key("\t", "RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("\t", "KeepAlive")
	b.WriteString("\t<true/>\n")
	key("\t", "StandardOutPath")
	str("\t", s.Home+"/.litellm.log")
	key("\t", "StandardErrorPath")
	str("\t", s.Home+"/.litellm.err.log")
	key("\t", "EnvironmentVariables")
	b.WriteString("\t<dict>\n")
	for _, kv := range [][2]string{{"PATH", s.Path}, {"LITELLM_MASTER_KEY", s.MasterKey}, {"LITELLM_SALT_KEY", s.SaltKey}} {
		key("\t\t", kv[0])
		str("\t\t", kv[1])
	}
	b.WriteString("\t</dict>\n</dict>\n</plist>\n")
	return []byte(b.String())
}

const masterKeyName = "LITELLM_MASTER_KEY"

// plainString matches one <string>text</string> or an empty self-closed
// <string/>, nothing else. The self-closed form is accepted on purpose: it is
// an unambiguous empty value, and a blanked key is just what a repair wants to
// fill. CDATA, comments and child elements inside the string do not match.
var plainString = regexp.MustCompile(`^(?:<string>[^<]*</string>|<string\s*/>)$`)

// envScan is what scanEnv learns from a plist: the string values of the
// root dict's EnvironmentVariables entries and where the master key's
// <string> element sits.
type envScan struct {
	vars       map[string]string
	masters    int // LITELLM_MASTER_KEY entries inside EnvironmentVariables
	start, end int // byte bounds of the master key's <string> element
	masterErr  error
}

// scanEnv walks the document with a real XML decoder: root <dict> ->
// EnvironmentVariables -> its <dict>. Comments and CDATA are never keys, and a
// key outside that dict does not count. It reads to the end of the document so
// a result that does not parse is an error. Errors never carry a value.
func scanEnv(data []byte) (envScan, error) {
	sc := envScan{vars: map[string]string{}}
	dec := xml.NewDecoder(bytes.NewReader(data))
	rootSeen, envSeen := false, false
	for !rootSeen {
		tok, err := dec.Token()
		if err != nil {
			return sc, fmt.Errorf("no root dict: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			rootSeen = true
		}
	}
	// nextEntry returns the next <key> text of the dict just entered, or
	// done at its end tag.
	nextEntry := func() (key string, done bool, err error) {
		for {
			tok, err := dec.Token()
			if err != nil {
				return "", false, fmt.Errorf("unterminated dict: %w", err)
			}
			switch t := tok.(type) {
			case xml.EndElement:
				return "", true, nil
			case xml.StartElement:
				if t.Name.Local != "key" {
					return "", false, errors.New("dict entry does not start with a key")
				}
				return key, false, dec.DecodeElement(&key, &t)
			}
		}
	}
	for {
		key, done, err := nextEntry()
		if err != nil {
			return sc, err
		}
		if done {
			break
		}
		var val xml.StartElement
	find:
		for {
			tok, err := dec.Token()
			if err != nil {
				return sc, fmt.Errorf("key without a value: %w", err)
			}
			if t, ok := tok.(xml.StartElement); ok {
				val = t
				break find
			}
		}
		if key != "EnvironmentVariables" || val.Name.Local != "dict" {
			if err := dec.Skip(); err != nil {
				return sc, err
			}
			continue
		}
		if envSeen {
			return sc, errors.New("EnvironmentVariables appears more than once")
		}
		envSeen = true
		for {
			name, done, err := nextEntry()
			if err != nil {
				return sc, err
			}
			if done {
				break
			}
			off := int(dec.InputOffset())
			clean := true
			var v xml.StartElement
		findv:
			for {
				tok, err := dec.Token()
				if err != nil {
					return sc, fmt.Errorf("variable without a value: %w", err)
				}
				switch t := tok.(type) {
				case xml.CharData:
					if len(bytes.TrimSpace(t)) != 0 {
						clean = false
					}
					if clean {
						off = int(dec.InputOffset())
					}
				case xml.Comment:
					clean = false
				case xml.StartElement:
					v = t
					break findv
				}
			}
			if v.Name.Local == "string" {
				var text string
				if err := dec.DecodeElement(&text, &v); err != nil {
					return sc, err
				}
				sc.vars[name] = text
			} else if err := dec.Skip(); err != nil {
				return sc, err
			}
			if name != masterKeyName {
				continue
			}
			sc.masters++
			sc.start, sc.end = off, int(dec.InputOffset())
			switch {
			case v.Name.Local != "string":
				sc.masterErr = errors.New("LITELLM_MASTER_KEY is not followed by a plain <string> value")
			case !clean || !plainString.Match(data[sc.start:sc.end]):
				sc.masterErr = errors.New("LITELLM_MASTER_KEY is not followed by a plain <string> value")
			}
		}
	}
	for { // the rest of the document must parse
		if _, err := dec.Token(); err != nil {
			if err == io.EOF {
				return sc, nil
			}
			return sc, fmt.Errorf("parse plist: %w", err)
		}
	}
}

// verifyReplaced checks an edit's output against the scan of its input: the
// document parses, the master key is exactly newKey, and every other
// environment value is unchanged.
func verifyReplaced(before envScan, out []byte, newKey string) error {
	after, err := scanEnv(out)
	if err != nil {
		return errors.New("the edited plist does not parse")
	}
	if after.masters != 1 || after.vars[masterKeyName] != newKey {
		return errors.New("the edited plist does not carry the new master key")
	}
	if len(after.vars) != len(before.vars) {
		return errors.New("the edit changed the set of environment variables")
	}
	for k, v := range before.vars {
		if k != masterKeyName && after.vars[k] != v {
			return errors.New("the edit changed another environment variable")
		}
	}
	return nil
}

// ReplaceMasterKey sets the value of the single LITELLM_MASTER_KEY entry of
// the root dict's EnvironmentVariables by a strict text edit: the entry is
// located with an XML decoder (a comment, a CDATA section or a key at another
// dict level is never a match) and every byte outside the replaced string
// element is kept. A binary plist, a missing or repeated key, or a value that
// is not one plain string is an error, as is a newKey XML 1.0 cannot hold. The
// result is re-read and must carry newKey with every other variable
// unchanged, or nothing is returned. No error carries a key.
func ReplaceMasterKey(plist []byte, newKey string) ([]byte, error) {
	if bytes.HasPrefix(plist, []byte("bplist")) {
		return nil, errors.New("binary plist is not supported")
	}
	if !validXMLText(newKey) {
		return nil, errors.New("the new key holds a character XML cannot carry")
	}
	sc, err := scanEnv(plist)
	if err != nil {
		return nil, fmt.Errorf("read plist: %w", err)
	}
	switch {
	case sc.masters == 0:
		return nil, errors.New("LITELLM_MASTER_KEY is not in the plist's EnvironmentVariables")
	case sc.masters > 1:
		return nil, errors.New("LITELLM_MASTER_KEY appears more than once in EnvironmentVariables")
	case sc.masterErr != nil:
		return nil, sc.masterErr
	}
	out := make([]byte, 0, len(plist)+len(newKey))
	out = append(out, plist[:sc.start]...)
	out = append(out, "<string>"+xmlEsc(newKey)+"</string>"...)
	out = append(out, plist[sc.end:]...)
	if err := verifyReplaced(sc, out, newKey); err != nil {
		return nil, err
	}
	return out, nil
}

// validXMLText reports whether s is valid UTF-8 made only of XML 1.0 Chars.
func validXMLText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == '\t', r == '\n', r == '\r':
		case r >= 0x20 && r <= 0xD7FF, r >= 0xE000 && r <= 0xFFFD, r >= 0x10000 && r <= 0x10FFFF:
		default:
			return false
		}
	}
	return true
}
