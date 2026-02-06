package llmutils

import (
	"net/http"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// HTTPClientWithUserIDHeader wraps an HTTP client to add user_id header from context
type HTTPClientWithUserIDHeader struct {
	Transport http.RoundTripper
}

// RoundTrip implements http.RoundTripper and adds user_id header from context
func (c *HTTPClientWithUserIDHeader) RoundTrip(req *http.Request) (*http.Response, error) {
	// Get user_id from context
	if userID, ok := model.GetUserIDFromContext(req.Context()); ok {
		// Add user_id to header
		req.Header.Set("X-User-ID", userID)
	}

	// Use the underlying transport
	if c.Transport != nil {
		return c.Transport.RoundTrip(req)
	}

	// Fallback to default transport
	return http.DefaultTransport.RoundTrip(req)
}

// NewHTTPClientWithUserIDHeader creates a new HTTP client that adds user_id header from context
func NewHTTPClientWithUserIDHeader(baseClient *http.Client) *http.Client {
	if baseClient == nil {
		baseClient = http.DefaultClient
	}

	var transport http.RoundTripper = baseClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	return &http.Client{
		Transport:     &HTTPClientWithUserIDHeader{Transport: transport},
		Timeout:       baseClient.Timeout,
		CheckRedirect: baseClient.CheckRedirect,
		Jar:           baseClient.Jar,
	}
}

// NewOpenAIClientWithUserIDHeader creates a new OpenAI client configured to add user_id header from context
func NewOpenAIClientWithUserIDHeader(apiKey string, baseURL string, baseHTTPClient *http.Client) *openai.Client {
	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}

	// Wrap HTTP client to add user_id header from context
	config.HTTPClient = NewHTTPClientWithUserIDHeader(baseHTTPClient)

	return openai.NewClientWithConfig(config)
}
