package config

// LaunchState is the `[launch]` table of wt's config.toml. Every key under it
// is legacy: exit_prompt configured the post-exit removal offer, which is
// gone (cleanup is user-driven now). No value is validated or consulted any
// more (R305a) — any spelling parses and round-trips a save unchanged — and
// IgnoredLaunchKeys names the key so callers can note it once per process.
type LaunchState struct {
	ExitPrompt string `toml:"exit_prompt"`
}

// IgnoredLaunchKeys names the legacy keys this config carries a non-empty
// value for: keys wt tolerates — they parse and survive every save — but no
// longer reads. `[launch] exit_prompt` is the only one today, and every one
// of its values counts (none is validated or consulted, R305a).
func (c *Config) IgnoredLaunchKeys() []string {
	if c != nil && c.LaunchTable != nil && c.LaunchTable.ExitPrompt != "" {
		return []string{"[launch] exit_prompt"}
	}
	return nil
}
