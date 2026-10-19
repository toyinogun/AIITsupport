package llm

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/toyinogun/AIITsupport/internal/config"
)

func TestNewRefusesUnbuiltProviders(t *testing.T) {
	if _, err := New(config.Agent{LLMProvider: "bedrock"}); !errors.Is(err, ErrProviderNotBuilt) {
		t.Errorf("bedrock: %v", err)
	}
	if _, err := New(config.Agent{LLMProvider: "anthropic"}); err == nil {
		t.Error("anthropic without a key succeeded")
	}
	if _, err := New(config.Agent{LLMProvider: "anthropic", AnthropicAPIKey: "sk-ant-test"}); err != nil {
		t.Errorf("anthropic: %v", err)
	}
}

// covers: AC-16. The key comes from config, never the environment, and the
// SDK retries as configured.
func TestOptionsUseOnlyTheGivenKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-env")
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("X-Api-Key"))
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := anthropic.NewClient(append(Options("sk-ant-from-config", 1), option.WithBaseURL(srv.URL))...)
	_, err := client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: "claude-test", MaxTokens: 16,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})
	if err == nil {
		t.Fatal("a 503 succeeded")
	}
	if len(keys) != 2 {
		t.Errorf("%d attempts, want 2 (one retry)", len(keys))
	}
	for _, k := range keys {
		if k != "sk-ant-from-config" {
			t.Errorf("sent key %q, want the configured one", k)
		}
	}
	if strings.Contains(err.Error(), "sk-ant") {
		t.Errorf("the error holds a key: %v", err)
	}
}
