package modelsetup

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// agentMarker is the XML comment that says a plist was written by wt.
const agentMarker = "managed by wt"

// AgentSpec is everything the wt-model LaunchAgent plist is built from.
// TokenFile is a path: the token itself never goes into the plist.
type AgentSpec struct {
	Binary, Listen, TokenFile, Path, Home string
}

// AgentPlist is what ParseAgentPlist reads back from an existing plist.
type AgentPlist struct {
	// Managed is true only when the marker comment is present and the label
	// is AgentLabel.
	Managed   bool
	Label     string
	Binary    string
	Listen    string
	TokenFile string
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// RenderAgentPlist renders the LaunchAgent plist deterministically.
func RenderAgentPlist(s AgentSpec) []byte {
	var b strings.Builder
	str := func(indent, v string) { b.WriteString(indent + "<string>" + xmlEsc(v) + "</string>\n") }
	key := func(k string) { b.WriteString("\t<key>" + k + "</key>\n") }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<!-- " + agentMarker + " -->\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	key("Label")
	str("\t", AgentLabel)
	key("ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, a := range []string{s.Binary, "serve", "--listen", s.Listen, "--token-file", s.TokenFile} {
		str("\t\t", a)
	}
	b.WriteString("\t</array>\n")
	key("RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("KeepAlive")
	b.WriteString("\t<true/>\n")
	key("StandardOutPath")
	str("\t", s.Home+"/.wt-model.log")
	key("StandardErrorPath")
	str("\t", s.Home+"/.wt-model.err.log")
	key("EnvironmentVariables")
	b.WriteString("\t<dict>\n\t\t<key>PATH</key>\n")
	str("\t\t", s.Path)
	b.WriteString("\t</dict>\n</dict>\n</plist>\n")
	return []byte(b.String())
}

// ParseAgentPlist reads an XML plist. A binary plist is an error.
func ParseAgentPlist(data []byte) (AgentPlist, error) {
	if bytes.HasPrefix(data, []byte("bplist")) {
		return AgentPlist{}, errors.New("binary plist is not supported")
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	// The decoder only knows the DOCTYPE as a Directive; fine for our walk.
	marker := false
	var top map[string]any
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return AgentPlist{}, fmt.Errorf("parse plist: %w", err)
		}
		switch t := tok.(type) {
		case xml.Comment:
			if strings.TrimSpace(string(t)) == agentMarker {
				marker = true
			}
		case xml.StartElement:
			if t.Name.Local == "dict" && top == nil {
				v, err := plistValue(dec, t)
				if err != nil {
					return AgentPlist{}, err
				}
				top, _ = v.(map[string]any)
			}
		}
	}
	if top == nil {
		return AgentPlist{}, errors.New("plist has no top-level dict")
	}
	var p AgentPlist
	p.Label, _ = top["Label"].(string)
	args, _ := top["ProgramArguments"].([]any)
	strs := make([]string, 0, len(args))
	for _, a := range args {
		s, _ := a.(string)
		strs = append(strs, s)
	}
	if len(strs) > 0 {
		p.Binary = strs[0]
	}
	for i := 1; i+1 < len(strs); i++ {
		switch strs[i] {
		case "--listen":
			p.Listen = strs[i+1]
		case "--token-file":
			p.TokenFile = strs[i+1]
		}
	}
	p.Managed = marker && p.Label == AgentLabel
	return p, nil
}

// plistValue reads one value whose start element was just consumed: a dict
// becomes map[string]any, an array []any, a string its text, booleans bool,
// anything else nil.
func plistValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		m := map[string]any{}
		cur := ""
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("parse plist: %w", err)
			}
			switch t := tok.(type) {
			case xml.EndElement:
				return m, nil
			case xml.StartElement:
				if t.Name.Local == "key" {
					k, err := plistText(dec)
					if err != nil {
						return nil, err
					}
					cur = k
					continue
				}
				v, err := plistValue(dec, t)
				if err != nil {
					return nil, err
				}
				m[cur] = v
			}
		}
	case "array":
		arr := []any{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("parse plist: %w", err)
			}
			switch t := tok.(type) {
			case xml.EndElement:
				return arr, nil
			case xml.StartElement:
				v, err := plistValue(dec, t)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
		}
	case "string":
		return plistText(dec)
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return start.Name.Local == "true", nil
	}
	return nil, dec.Skip()
}

// plistText reads character data up to the element's end.
func plistText(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("parse plist: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return b.String(), nil
		}
	}
}
