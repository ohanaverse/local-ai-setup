package modelsetup

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
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

// LiteLLMSpec is everything the minimal LiteLLM LaunchAgent plist is built
// from. MasterKey and SaltKey are secrets: String and GoString redact them.
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

// RenderLiteLLMPlist renders the minimal LiteLLM LaunchAgent (the guide's
// plist without the optional keys) deterministically. The keys appear only
// as values of their EnvironmentVariables entries.
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

const masterKeyElem = "<key>LITELLM_MASTER_KEY</key>"

// masterValue matches, right after the key element, optional whitespace and
// one <string>text</string> or an empty self-closed <string/>. The self-closed
// form is accepted on purpose: it is an unambiguous empty value, and a blanked
// key is just what a repair wants to fill. CDATA, comments and child elements
// inside the string do not match and are refused.
var masterValue = regexp.MustCompile(`^\s*(?:<string>[^<]*</string>|<string\s*/>)`)

// ReplaceMasterKey sets the value of the single LITELLM_MASTER_KEY entry by a
// strict text edit: every byte outside the replaced string element is kept.
// A binary plist, a missing or repeated key, or a value that is not one
// plain string is an error, as is a newKey XML 1.0 cannot hold. No error
// carries a key.
func ReplaceMasterKey(plist []byte, newKey string) ([]byte, error) {
	if bytes.HasPrefix(plist, []byte("bplist")) {
		return nil, errors.New("binary plist is not supported")
	}
	if !validXMLText(newKey) {
		return nil, errors.New("the new key holds a character XML cannot carry")
	}
	switch n := bytes.Count(plist, []byte(masterKeyElem)); {
	case n == 0:
		return nil, errors.New("LITELLM_MASTER_KEY is not in the plist")
	case n > 1:
		return nil, errors.New("LITELLM_MASTER_KEY appears more than once in the plist")
	}
	end := bytes.Index(plist, []byte(masterKeyElem)) + len(masterKeyElem)
	loc := masterValue.FindIndex(plist[end:])
	if loc == nil {
		return nil, errors.New("LITELLM_MASTER_KEY is not followed by a plain <string> value")
	}
	// Keep the whitespace between the key and the element.
	ws := bytes.Index(plist[end:end+loc[1]], []byte("<"))
	start, stop := end+ws, end+loc[1]
	out := make([]byte, 0, len(plist)+len(newKey))
	out = append(out, plist[:start]...)
	out = append(out, "<string>"+xmlEsc(newKey)+"</string>"...)
	out = append(out, plist[stop:]...)
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
