package modelmode

import (
	"encoding/json"
	"strconv"
)

// Secret holds a secret the user typed (a pasted token, a master key). Every
// printed or marshalled form is the redaction "[redacted]" (empty stays
// empty, so absence is visible): fmt's %v, %+v, %#v, %s and %q, JSON and
// text marshalling. Reveal returns the value; Apply and the tab call it only
// at the point of use, never to log or show it.
type Secret string

const redacted = "[redacted]"

func (s Secret) redact() string {
	if s == "" {
		return ""
	}
	return redacted
}

// String redacts the secret.
func (s Secret) String() string { return s.redact() }

// GoString redacts the secret for %#v.
func (s Secret) GoString() string { return strconv.Quote(s.redact()) }

// MarshalJSON redacts the secret.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.redact()) }

// MarshalText redacts the secret.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.redact()), nil }

// Reveal returns the secret itself; call it only where it is used.
func (s Secret) Reveal() string { return string(s) }
