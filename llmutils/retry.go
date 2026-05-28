package llmutils

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
)

const (
	// DefaultLLMMaxRetries is the default number of retries for LLM calls (3 attempts total).
	DefaultLLMMaxRetries = 3
	// DefaultLLMInitialBackoff is the initial backoff duration before first retry.
	DefaultLLMInitialBackoff = 1 * time.Second
)

// isRetriableError returns true if the error is transient and worth retrying.
func isRetriableError(err error) bool {
	if err == nil {
		return false
	}
	// Network / connection errors
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	if strings.Contains(err.Error(), "connection reset by peer") ||
		strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "i/o timeout") ||
		strings.Contains(err.Error(), "EOF") {
		return true
	}
	// OpenAI API errors: 429 (rate limit), 5xx (server error)
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		if apiErr.HTTPStatusCode == 429 {
			return true
		}
		if apiErr.HTTPStatusCode >= 500 {
			return true
		}
	}
	return false
}

// CreateChatCompletionWithRetry calls client.CreateChatCompletion with up to DefaultLLMMaxRetries
// attempts and exponential backoff on retriable errors. All LLM calls should go through this
// for consistent resilience (e.g. connection reset by peer).
func CreateChatCompletionWithRetry(ctx context.Context, client LLMClient, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	var lastErr error
	backoff := DefaultLLMInitialBackoff
	for attempt := 0; attempt < DefaultLLMMaxRetries; attempt++ {
		resp, err := client.CreateChatCompletion(ctx, request)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetriableError(err) || attempt == DefaultLLMMaxRetries-1 {
			return openai.ChatCompletionResponse{}, err
		}
		select {
		case <-ctx.Done():
			return openai.ChatCompletionResponse{}, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}
	return openai.ChatCompletionResponse{}, lastErr
}
