package modelmode

import "path/filepath"

// DefaultListen is the address the model server listens on by default.
const DefaultListen = "127.0.0.1:8787"

// DefaultLocalTokenPath is where the local plan creates the model server's
// token: <config home>/wt-model/server.token.
func DefaultLocalTokenPath(configHome string) string {
	return filepath.Join(configHome, "wt-model", "server.token")
}

// RemoteTokenPath is the one file a pasted remote token is written to:
// <config home>/agent-wt/remote-model-server.token.
func RemoteTokenPath(configHome string) string {
	return filepath.Join(configHome, "agent-wt", "remote-model-server.token")
}
