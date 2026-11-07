package config

import "fmt"

// LaunchState is the `[launch]` table of wt's config.toml: settings for what
// wt does around an agent launch. ExitPrompt is "ask" (the default; an
// untouched worktree this launch created is offered for removal when the
// agent exits) or "keep" (never offer). No value removes without a keypress.
type LaunchState struct {
	ExitPrompt string `toml:"exit_prompt"`
}

// ExitPromptKeep reports whether the user turned the exit prompt off
// (`[launch] exit_prompt = "keep"`). Absent, empty and "ask" all mean ask.
func (c *Config) ExitPromptKeep() bool {
	return c != nil && c.LaunchTable != nil && c.LaunchTable.ExitPrompt == "keep"
}

// validateLaunch rejects an exit_prompt value other than "", "ask" or "keep",
// naming the key and the allowed values.
func (c *Config) validateLaunch() error {
	if c.LaunchTable == nil {
		return nil
	}
	switch c.LaunchTable.ExitPrompt {
	case "", "ask", "keep":
		return nil
	}
	return fmt.Errorf("config.toml: [launch] exit_prompt = %q is not recognised; allowed values are \"ask\" and \"keep\"", c.LaunchTable.ExitPrompt)
}
