// Package tokenfile loads the shared bearer token that wt-model and its
// session clients read from a private file.
package tokenfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Kind says which check a token file failed.
type Kind int

const (
	KindStat  Kind = iota // the file could not be stat'ed
	KindRead              // the file could not be read
	KindEmpty             // the file holds no token
	KindLoose             // group or other can access the file
)

// Error is a token file failure. Only a KindLoose error carries a complete
// sentence with its way out; the other kinds carry the path and the OS
// reason, and each caller adds its own way out.
type Error struct {
	Path   string
	Kind   Kind
	Detail string // the OS reason, or the mode for KindLoose
}

func (e *Error) Error() string {
	switch e.Kind {
	case KindLoose:
		return fmt.Sprintf("token file %s is accessible by group or other (mode %s); run 'chmod 600 %s'", e.Path, e.Detail, e.Path)
	case KindEmpty:
		return fmt.Sprintf("token file %s is empty", e.Path)
	default:
		return fmt.Sprintf("token file %s: %s", e.Path, e.Detail)
	}
}

// reason is why a file call failed without the operation and path.
func reason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// Load reads the token from path. The file must not be accessible by group
// or other (mode 0600 or stricter) and must hold a non-empty token;
// surrounding whitespace is trimmed. A failure is an *Error.
func Load(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", &Error{Path: path, Kind: KindStat, Detail: reason(err)}
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", &Error{Path: path, Kind: KindLoose, Detail: fmt.Sprintf("%04o", st.Mode().Perm())}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", &Error{Path: path, Kind: KindRead, Detail: reason(err)}
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", &Error{Path: path, Kind: KindEmpty}
	}
	return tok, nil
}
