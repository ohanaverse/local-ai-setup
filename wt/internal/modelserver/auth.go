package modelserver

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tokenfile"
)

// tokenCreateCmd creates a token file, the way out of a missing or empty one.
const tokenCreateCmd = "umask 077; head -c 32 /dev/urandom | base64 > PATH"

// LoadToken reads the shared bearer token from path (see tokenfile.Load) and
// words a failure for the server: it names --token-file and the recipe that
// creates a token file.
func LoadToken(path string) (string, error) {
	tok, err := tokenfile.Load(path)
	if err == nil {
		return tok, nil
	}
	var te *tokenfile.Error
	if !errors.As(err, &te) {
		return "", err
	}
	switch te.Kind {
	case tokenfile.KindStat:
		return "", fmt.Errorf("token file %s: %s (create one with '%s' or pass --token-file PATH)", path, te.Detail, tokenCreateCmd)
	case tokenfile.KindRead:
		return "", fmt.Errorf("token file %s: %s (check its permissions or pass --token-file PATH)", path, te.Detail)
	case tokenfile.KindEmpty:
		return "", fmt.Errorf("token file %s is empty (create a token with '%s')", path, tokenCreateCmd)
	}
	return "", err
}

// writeError writes the APIError body every non-2xx control response carries.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(modelapi.APIError{Error: modelapi.ErrorObject{Message: msg, Code: code}})
}

// requireToken wraps next so a request without the bearer token is a 401 and
// next never runs. The token is never empty (NewServer refuses that).
func requireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeError(w, http.StatusUnauthorized, modelapi.CodeUnauthorized, modelapi.MsgUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
