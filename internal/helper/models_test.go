package helper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestModelsURL(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"https://api.openai.com", "/v1/chat/completions", "https://api.openai.com/v1/models"},
		{"https://api.openai.com", "/v1/responses", "https://api.openai.com/v1/models"},
		{"https://api.anthropic.com", "/v1/messages", "https://api.anthropic.com/v1/models"},
		{"https://example.com/api", "/chat/completions", "https://example.com/api/models"},
	}
	for _, tc := range cases {
		if got := modelsURL(tc.base, tc.path); got != tc.want {
			t.Fatalf("modelsURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

func TestFetchModelsProtocols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer test-key" {
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-test"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-test"}],"has_more":false}`))
	}))
	defer server.Close()

	config := model.ChannelConfig{
		BaseURL:                  server.URL,
		OpenAIChatCompletionPath: "/v1/chat/completions",
		AnthropicMessagePath:     "/v1/messages",
	}
	models, err := FetchModels(context.Background(), config, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]model.Protocol, len(models))
	for _, m := range models {
		got[m.Name] = m.Protocols
	}
	if got["gpt-test"] != model.ProtocolOpenAIChatCompletion {
		t.Fatalf("gpt-test = %d", got["gpt-test"])
	}
	if got["claude-test"] != model.ProtocolAnthropicMessage {
		t.Fatalf("claude-test = %d", got["claude-test"])
	}
}