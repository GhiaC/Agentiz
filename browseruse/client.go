package browseruse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxResponseBytes = int64(2 << 20)
	sessionHeader           = "X-Agentize-Session-ID"
)

// Config configures the HTTP sidecar client.
type Config struct {
	BaseURL          string
	Token            string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

// Client is an HTTP implementation of Service.
type Client struct {
	baseURL          *url.URL
	token            string
	httpClient       *http.Client
	maxResponseBytes int64
}

// APIError is returned for a non-2xx response from the sidecar.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("browser-use service returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("browser-use service returned HTTP %d: %s", e.StatusCode, e.Message)
}

// NewClient validates config and creates a sidecar client.
func NewClient(config Config) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("parse browser-use base URL: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("browser-use base URL must use http or https")
	}
	if baseURL.Host == "" {
		return nil, errors.New("browser-use base URL must include a host")
	}
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")

	token := strings.TrimSpace(config.Token)
	if token == "" {
		return nil, errors.New("browser-use sidecar token is required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}

	return &Client{
		baseURL:          baseURL,
		token:            token,
		httpClient:       httpClient,
		maxResponseBytes: maxResponseBytes,
	}, nil
}

// Health checks whether the sidecar HTTP process is ready.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", "", nil, nil)
}

// Start creates an asynchronous browser job.
func (c *Client) Start(ctx context.Context, sessionID string, request StartJobRequest) (*Job, error) {
	if strings.TrimSpace(request.Task) == "" {
		return nil, errors.New("browser-use task is required")
	}
	var job Job
	if err := c.do(ctx, http.MethodPost, "/v1/jobs", sessionID, request, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// Get returns a job snapshot. wait enables bounded server-side long polling.
func (c *Client) Get(ctx context.Context, sessionID, jobID string, wait time.Duration) (*Job, error) {
	path, err := jobPath(jobID)
	if err != nil {
		return nil, err
	}
	if wait < 0 {
		return nil, errors.New("browser-use wait duration cannot be negative")
	}
	if wait > 60*time.Second {
		wait = 60 * time.Second
	}
	if wait > 0 {
		path += "?wait_seconds=" + strconv.FormatFloat(wait.Seconds(), 'f', 3, 64)
	}
	var job Job
	if err := c.do(ctx, http.MethodGet, path, sessionID, nil, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// Cancel requests cancellation and returns the resulting job snapshot.
func (c *Client) Cancel(ctx context.Context, sessionID, jobID string) (*Job, error) {
	path, err := jobPath(jobID)
	if err != nil {
		return nil, err
	}
	var job Job
	if err := c.do(ctx, http.MethodPost, path+"/cancel", sessionID, nil, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func jobPath(jobID string) (string, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return "", errors.New("browser-use job ID is required")
	}
	for _, char := range jobID {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' {
			return "", errors.New("browser-use job ID contains invalid characters")
		}
	}
	return "/v1/jobs/" + jobID, nil
}

func (c *Client) do(
	ctx context.Context,
	method, path, sessionID string,
	requestBody interface{},
	responseBody interface{},
) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode browser-use request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(c.baseURL.Path, "/") + path
	if queryIndex := strings.IndexByte(path, '?'); queryIndex >= 0 {
		endpoint.Path = strings.TrimRight(c.baseURL.Path, "/") + path[:queryIndex]
		endpoint.RawQuery = path[queryIndex+1:]
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create browser-use request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.token)
	if requestBody != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	if sessionID != "" {
		httpRequest.Header.Set(sessionHeader, sessionID)
	}

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("call browser-use service: %w", err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, c.maxResponseBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read browser-use response: %w", err)
	}
	if int64(len(payload)) > c.maxResponseBytes {
		return errors.New("browser-use response exceeded configured size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return decodeAPIError(response.StatusCode, payload)
	}
	if responseBody == nil || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, responseBody); err != nil {
		return fmt.Errorf("decode browser-use response: %w", err)
	}
	return nil
}

func decodeAPIError(statusCode int, payload []byte) error {
	var body struct {
		Detail interface{} `json:"detail"`
	}
	message := strings.TrimSpace(string(payload))
	if json.Unmarshal(payload, &body) == nil && body.Detail != nil {
		switch detail := body.Detail.(type) {
		case string:
			message = detail
		default:
			if encoded, err := json.Marshal(detail); err == nil {
				message = string(encoded)
			}
		}
	}
	if len(message) > 1000 {
		message = message[:1000] + "..."
	}
	return &APIError{StatusCode: statusCode, Message: message}
}
