package litellm

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
)

// ProxyLabel is the proxy LaunchAgent's Label, exported for the setup code
// that names the agent (the unexported proxyLabel is the one definition).
const ProxyLabel = proxyLabel

// ProxyPlistInfo is what the proxy LaunchAgent plist says about how the proxy
// runs. It is read-only evidence for setup checks and plans.
//
// EnvNames holds the NAMES of the EnvironmentVariables, sorted, and never a
// value: the values include API keys. ReadProxyPlistVar is the single way to
// read one value.
type ProxyPlistInfo struct {
	Path             string
	Label            string
	ProgramArguments []string
	// Port is the value after --port in ProgramArguments; 0 when absent or
	// not a number.
	Port int
	// ConfigPath is the value after --config; "" when absent.
	ConfigPath string
	RunAtLoad  bool
	// KeepAlive is true only for the literal <true/> element; a KeepAlive
	// dict (conditional keep-alive) reads false.
	KeepAlive bool
	EnvNames  []string
	// Mode is the file's permission bits from os.Stat.
	Mode os.FileMode
}

// ReadProxyPlist reads the XML property list at path. A binary plist is an
// error that names the conversion. No environment value is kept or quoted.
func ReadProxyPlist(path string) (ProxyPlistInfo, error) {
	info := ProxyPlistInfo{Path: path}
	st, err := os.Stat(path)
	if err != nil {
		return info, err
	}
	info.Mode = st.Mode().Perm()
	data, err := readXMLPlist(path)
	if err != nil {
		return info, err
	}
	dec, err := plistRootDict(data)
	if err != nil {
		return info, fmt.Errorf("%s: %w", path, err)
	}
	info.EnvNames = []string{}
	err = eachDictEntry(dec, func(key string, v xml.StartElement) error {
		switch {
		case key == "Label" && v.Name.Local == "string":
			return dec.DecodeElement(&info.Label, &v)
		case key == "ProgramArguments" && v.Name.Local == "array":
			args, err := decodeStringArray(dec)
			info.ProgramArguments = args
			return err
		case key == "RunAtLoad":
			info.RunAtLoad = v.Name.Local == "true"
			return dec.Skip()
		case key == "KeepAlive":
			info.KeepAlive = v.Name.Local == "true"
			return dec.Skip()
		case key == "EnvironmentVariables" && v.Name.Local == "dict":
			return eachDictEntry(dec, func(name string, _ xml.StartElement) error {
				info.EnvNames = append(info.EnvNames, name)
				return dec.Skip() // the value is never decoded
			})
		}
		return dec.Skip()
	})
	if err != nil {
		return info, fmt.Errorf("%s: %w", path, err)
	}
	sort.Strings(info.EnvNames)
	for i := 0; i+1 < len(info.ProgramArguments); i++ {
		switch info.ProgramArguments[i] {
		case "--port":
			if n, err := strconv.Atoi(info.ProgramArguments[i+1]); err == nil {
				info.Port = n
			}
		case "--config":
			info.ConfigPath = info.ProgramArguments[i+1]
		}
	}
	return info, nil
}

// ReadProxyPlistVar returns the value of one EnvironmentVariables entry, and
// whether it is set. It is the single way to read one value: callers must
// never print it, log it or put it in an error.
func ReadProxyPlistVar(path, name string) (value string, ok bool, err error) {
	data, err := readXMLPlist(path)
	if err != nil {
		return "", false, err
	}
	vars, err := plistEnvironment(data)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", path, err)
	}
	value, ok = vars[name]
	return value, ok, nil
}

func readXMLPlist(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		return nil, fmt.Errorf("%s is a binary property list; convert it with `plutil -convert xml1 %s`", path, path)
	}
	return data, nil
}

// plistRootDict positions a decoder just inside the root <dict>.
func plistRootDict(data []byte) (*xml.Decoder, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("no root dict: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			return dec, nil
		}
	}
}

// decodeStringArray reads the <string> children of the <array> whose start
// tag was just read, through its end tag; other elements are skipped.
func decodeStringArray(dec *xml.Decoder) ([]string, error) {
	out := []string{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return out, errors.Join(errors.New("unterminated array"), err)
		}
		switch el := tok.(type) {
		case xml.EndElement:
			return out, nil
		case xml.StartElement:
			if el.Name.Local != "string" {
				if err := dec.Skip(); err != nil {
					return out, err
				}
				continue
			}
			var s string
			if err := dec.DecodeElement(&s, &el); err != nil {
				return out, err
			}
			out = append(out, s)
		}
	}
}
