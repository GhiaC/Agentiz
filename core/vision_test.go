package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

// blockingCallback denies every action with a fixed message — a stand-in for an
// over-quota billing callback.
type blockingCallback struct{ msg string }

func (c *blockingCallback) BeforeAction(context.Context, *engine.UsageEvent) error {
	return errors.New(c.msg)
}
func (c *blockingCallback) AfterAction(context.Context, *engine.UsageEvent) {}

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

// visionRecordingCallback captures billing events for the vision path.
type visionRecordingCallback struct {
	before, after *engine.UsageEvent
}

func (c *visionRecordingCallback) BeforeAction(_ context.Context, ev *engine.UsageEvent) error {
	c.before = ev
	return nil
}
func (c *visionRecordingCallback) AfterAction(_ context.Context, ev *engine.UsageEvent) { c.after = ev }

func TestProcessMessageWithImage_Success(t *testing.T) {
	transport := &MockLLMTransport{}
	transport.AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: "I see a cat in the image",
			},
		}},
		Usage: openai.Usage{PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100},
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
	cb := &visionRecordingCallback{}
	ch.SetCallback(cb)

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

	// The vision (image-input) LLM call must be billed — previously it was not.
	if cb.before == nil || cb.before.EventType != engine.EventLLMCall || cb.before.Model != "vision-model" {
		t.Errorf("BeforeAction not fired for vision LLM: %+v", cb.before)
	}
	if cb.after == nil {
		t.Fatal("AfterAction not fired for vision LLM")
	}
	if cb.after.InputTokens != 80 || cb.after.OutputTokens != 20 || cb.after.Tokens != 100 {
		t.Errorf("vision usage wrong: in=%d out=%d total=%d", cb.after.InputTokens, cb.after.OutputTokens, cb.after.Tokens)
	}
	if cb.after.Metadata["media"] != "image" {
		t.Errorf("expected media=image metadata, got %v", cb.after.Metadata)
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

// TestProcessMessageWithImage_RecordsRouteTrace verifies the vision path (C8)
// actually persists a route trace, so image messages appear in the routing DAG
// like text messages. It uses a real SQLiteStore — the MockSessionStore in the
// other vision tests does not implement route-trace persistence, so persistence
// would silently no-op there and a regression (a removed rec.Decision/Response
// or the persist defer) would go unnoticed.
func TestProcessMessageWithImage_RecordsRouteTrace(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	cfg := model.DefaultSessionHandlerConfig()
	cfg.DisableLogs = true
	sh := model.NewSessionHandler(sqliteStore, cfg)
	am := agentmanager.New(sh)
	ch := NewCoreHandler(sh, am, DefaultCoreHandlerConfig())

	transport := &MockLLMTransport{}
	transport.AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message:      openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "a cat"},
			FinishReason: openai.FinishReasonStop,
		}},
		Usage: openai.Usage{PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100},
	})
	if err := ch.UseLLMConfig(engine.LLMConfig{
		APIKey: "test", Model: "main-model",
		HTTPClient: &http.Client{Transport: transport}, BackupDisabled: true,
	}); err != nil {
		t.Fatalf("UseLLMConfig: %v", err)
	}
	if err := ch.UseVisionLLMConfig(engine.LLMConfig{
		APIKey: "test", Model: "vision-model",
		HTTPClient: &http.Client{Transport: transport},
	}); err != nil {
		t.Fatalf("UseVisionLLMConfig: %v", err)
	}
	ch.userModeration = nil // skip the nonsense check so it doesn't consume a mock response

	resp, err := ch.ProcessMessageWithImage(context.Background(), "user1", "what is this?", []byte("img"), "image/png")
	if err != nil {
		t.Fatalf("ProcessMessageWithImage: %v", err)
	}
	if resp != "a cat" {
		t.Fatalf("response = %q, want %q", resp, "a cat")
	}

	traces, err := sqliteStore.GetRouteTracesByUser("user1")
	if err != nil {
		t.Fatalf("GetRouteTracesByUser: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1 (the vision path must record a route trace)", len(traces))
	}
	tr := traces[0]
	if tr.Status != "ok" {
		t.Errorf("status = %q, want ok", tr.Status)
	}
	if !strings.Contains(tr.Message, "image") {
		t.Errorf("trace message should mention the image, got %q", tr.Message)
	}
	if tr.Response != "a cat" {
		t.Errorf("response = %q, want %q", tr.Response, "a cat")
	}
	if tr.TotalTokens != 100 {
		t.Errorf("total tokens = %d, want 100", tr.TotalTokens)
	}
	var hasUser, hasDecision, hasResponse bool
	for _, n := range tr.Nodes {
		switch n.Type {
		case model.RouteNodeUserMessage:
			hasUser = true
		case model.RouteNodeDecision:
			hasDecision = true
		case model.RouteNodeResponse:
			hasResponse = true
		}
	}
	if !hasUser || !hasDecision || !hasResponse {
		t.Errorf("missing node types: user=%v decision=%v response=%v (nodes=%+v)", hasUser, hasDecision, hasResponse, tr.Nodes)
	}
}

// TestProcessMessageWithImage_EarlyQuotaCheck verifies C4 on the vision path: an
// over-quota user is rejected BEFORE the image is recorded and the system prompt
// is built (and before the vision LLM runs), so the build cost is not paid. The
// file recorder running would prove the rejection came too late.
func TestProcessMessageWithImage_EarlyQuotaCheck(t *testing.T) {
	transport := &MockLLMTransport{}
	ch := newVisionTestHandler(t, transport)
	visionCfg := engine.LLMConfig{APIKey: "test", Model: "vision-model", HTTPClient: &http.Client{Transport: transport}}
	if err := ch.UseVisionLLMConfig(visionCfg); err != nil {
		t.Fatalf("UseVisionLLMConfig: %v", err)
	}
	ch.SetCallback(&blockingCallback{msg: "over quota"})

	recorded := 0
	ch.SetFileRecorder(func(string, string, string, model.FileSource, []byte) (*model.UserFile, error) {
		recorded++
		return &model.UserFile{}, nil
	})

	resp, err := ch.ProcessMessageWithImage(context.Background(), "user1", "what is this?", []byte("img"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "over quota" {
		t.Errorf("response = %q, want the block message", resp)
	}
	if recorded != 0 {
		t.Errorf("over-quota user must be rejected before the image is recorded, got %d recordings", recorded)
	}
	if transport.Calls() != 0 {
		t.Errorf("vision LLM must not run when the early quota check blocks, got %d calls", transport.Calls())
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
