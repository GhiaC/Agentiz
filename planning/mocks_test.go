package planning

import (
	"context"

	"github.com/sashabaranov/go-openai"
)

// mockChatClient is the shared test double for the OpenAI chat-completion client
// (CreateChatCompletion). It serves both the LLM planner and the
// orchestrator/runner direct-LLM paths, replacing the previously duplicated
// mockLLMClient and mockDirectLLMClient.
type mockChatClient struct {
	response string // content returned in the single choice
	err      error  // when set, CreateChatCompletion returns it
	onCall   func() // optional hook invoked on each call
}

func (m *mockChatClient) CreateChatCompletion(_ context.Context, _ openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if m.onCall != nil {
		m.onCall()
	}
	if m.err != nil {
		return openai.ChatCompletionResponse{}, m.err
	}
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: m.response}}},
		Usage:   openai.Usage{TotalTokens: 5},
	}, nil
}
