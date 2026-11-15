package modelserver

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// LoadToken reads the shared bearer token from path. The file must not be
// readable or writable by group or other (mode 0600 or stricter), and must
// hold a non-empty token; surrounding whitespace is trimmed.
func LoadToken(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file %s is accessible by group or other (mode %04o); run: chmod 600 %s", path, st.Mode().Perm(), path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("token file " + path + " is empty")
	}
	return tok, nil
}

// writeError writes the APIError body every non-2xx control response carries.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(modelapi.APIError{Error: msg, Code: code})
}

// requireToken wraps next so a request without the bearer token is a 401 and
// next never runs. The token is never empty (NewServer refuses that).
func requireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or wrong bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}
