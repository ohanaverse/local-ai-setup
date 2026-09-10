package config

// Protocol identifies a wire protocol an agent CLI or provider endpoint
// speaks. Empty-intersection between an agent's and a provider's protocol
// sets means the pairing cannot be dialed direct and must route through
// LiteLLM regardless of the on/off setting.
type Protocol string

const (
	ProtocolAnthropic       Protocol = "anthropic"
	ProtocolOpenAIChat      Protocol = "openai-chat"
	ProtocolOpenAIResponses Protocol = "openai-responses"
)
