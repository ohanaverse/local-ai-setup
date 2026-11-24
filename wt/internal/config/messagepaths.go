package config

// The files a refusal or a hint tells the user to edit (#700): the resolved
// path, so the message names the file that is actually read when
// XDG_CONFIG_HOME moves the config home, and the conventional ~/.config path
// only when there is no config home at all (HOME and XDG_CONFIG_HOME unset).
// Help text and usage examples describe the default and keep the literal.

// conventionalWTModelConfig and conventionalAgentConfig are what a message
// says when no config home can be resolved.
const (
	conventionalWTModelConfig = "~/.config/wt-model/config.toml"
	conventionalAgentConfig   = "~/.config/agent-wt/config.toml"
)

// ProxyHomeFile names the wt-model home's config.toml ([litellm] and
// [gateway]) for a message.
func ProxyHomeFile() string {
	if p := ProxyHomePath(); p != "" {
		return p
	}
	return conventionalWTModelConfig
}

// ConfigFile names agent-wt's config.toml ([model-server], the agents) for a
// message.
func ConfigFile() string {
	if baseConfigHome() == "" {
		return conventionalAgentConfig
	}
	return Path()
}
