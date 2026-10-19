// Package llm builds the Anthropic SDK client the agent calls Claude with
// (spec 0001, spec 0008). It defines no request or response types of its
// own: the agent uses the SDK's. The client never reads the environment; the
// key and retries come from internal/config.
package llm

import (
	"errors"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/toyinogun/AIITsupport/internal/config"
)

// ErrProviderNotBuilt means LLM_PROVIDER names a provider this build does
// not support yet (bedrock waits on the EU residency decision, spec 0001).
var ErrProviderNotBuilt = errors.New("LLM_PROVIDER bedrock is not built yet; use anthropic")

// New returns the SDK client for cfg.LLMProvider. Its options come only from
// cfg: environment defaults (ANTHROPIC_* variables, profiles) are off, and
// the SDK's spans carry metadata only, never message content (AC-17).
func New(cfg config.Agent) (anthropic.Client, error) {
	switch cfg.LLMProvider {
	case "anthropic":
	case "bedrock":
		return anthropic.Client{}, ErrProviderNotBuilt
	default:
		return anthropic.Client{}, errors.New("LLM_PROVIDER must be anthropic or bedrock")
	}
	if cfg.AnthropicAPIKey == "" {
		return anthropic.Client{}, errors.New("ANTHROPIC_API_KEY is required for LLM_PROVIDER anthropic")
	}
	return anthropic.NewClient(Options(cfg.AnthropicAPIKey, cfg.MaxRetries)...), nil
}

// Options are the client options New uses, exported so tests can point the
// same client at a fake server with option.WithBaseURL.
func Options(apiKey string, maxRetries int) []option.RequestOption {
	return []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(maxRetries),
		option.WithHTTPClient(&http.Client{}),
		option.WithOpenTelemetryTracesContentMode(option.OpenTelemetryContentModeMetadataOnly),
		// The trace context stays inside Noord's systems.
		option.WithOpenTelemetryPropagation(false),
	}
}
