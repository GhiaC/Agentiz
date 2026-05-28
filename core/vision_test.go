package core

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

func newVisionTestHandler(t *testing.T, transport *MockLLMTransport) *CoreHandler {
	t.Helper()
	mockStore := NewMockSessionStore()
	config := model.DefaultSessionHandlerConfig()
	config.DisableLogs = true
	sh := model.NewSessionHandler(mockStore, config)
	am := agentmanager.New(sh)
	ch := NewCoreHandler(sh, am, DefaultCoreHandlerConfig())
	cfg := engine.LLMConfig{
		APIKey:         "test",
		Model:          "test-model",
		HTTPClient:     &http.Client{Transport: transport},
		BackupDisabled: true,
	}
	if err := ch.UseLLMConfig(cfg); err != nil {
		t.Fatalf("UseLLMConfig: %v", err)
	}
	return ch
}

func TestProcessMessageWithImage_Success(t *testing.T) {
	transport := &MockLLMTransport{}
	transport.AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: "I see a cat in the image",
			},
		}},
		Usage: openai.Usage{TotalTokens: 100},
	})

	ch := newVisionTestHandler(t, transport)
	visionCfg := engine.LLMConfig{
		APIKey:     "test",
		Model:      "vision-model",
		HTTPClient: &http.Client{Transport: transport},
	}
	if err := ch.UseVisionLLMConfig(visionCfg); err != nil {
		t.Fatalf("UseVisionLLMConfig: %v", err)
	}

	resp, err := ch.ProcessMessageWithImage(
		context.Background(),
		"user1",
		"what is this?",
		[]byte("fake-image-data"),
		"image/png",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "I see a cat in the image" {
		t.Errorf("unexpected response: %q", resp)
	}
}

func TestProcessMessageWithImage_FallbackToMainLLM(t *testing.T) {
	transport := &MockLLMTransport{}
	transport.AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: "fallback response",
			},
		}},
	})

	ch := newVisionTestHandler(t, transport)
	// Do NOT set vision LLM — should fall back to main

	resp, err := ch.ProcessMessageWithImage(
		context.Background(),
		"user1",
		"describe this",
		[]byte("fake-image-data"),
		"image/jpeg",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "fallback response" {
		t.Errorf("unexpected response: %q", resp)
	}
}

func TestProcessMessageWithImage_NotReady(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	_, err := ch.ProcessMessageWithImage(
		context.Background(),
		"user1",
		"hello",
		[]byte("img"),
		"image/png",
	)
	if err == nil {
		t.Fatal("expected error when agents not ready")
	}
	if !strings.Contains(err.Error(), "ready") && !strings.Contains(err.Error(), "Init") {
		t.Errorf("expected ready/Init error, got %q", err.Error())
	}
}
