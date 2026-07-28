package browseruse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func testHTTPClient(handler roundTripFunc) *http.Client {
	return &http.Client{Transport: handler}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestClientStartSendsAuthOwnershipAndRequest(t *testing.T) {
	t.Parallel()

	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/jobs" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("unexpected authorization: %q", got)
		}
		if got := request.Header.Get(sessionHeader); got != "session-42" {
			t.Fatalf("unexpected session header: %q", got)
		}
		var input StartJobRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Task != "inspect example.com" || input.MaxSteps != 12 {
			t.Fatalf("unexpected input: %#v", input)
		}
		return jsonResponse(
			http.StatusAccepted,
			`{"id":"job-1","status":"queued","created_at":"2026-07-28T10:00:00Z"}`,
		), nil
	})

	client, err := NewClient(Config{
		BaseURL:    "http://browser-use.test",
		Token:      "secret",
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := client.Start(context.Background(), "session-42", StartJobRequest{
		Task:     "inspect example.com",
		MaxSteps: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != "job-1" || job.Status != JobQueued {
		t.Fatalf("unexpected job: %#v", job)
	}
}

func TestClientGetUsesBoundedLongPoll(t *testing.T) {
	t.Parallel()

	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if got := request.URL.Query().Get("wait_seconds"); got != "60.000" {
			t.Fatalf("unexpected wait_seconds: %q", got)
		}
		return jsonResponse(
			http.StatusOK,
			`{"id":"job-1","status":"running","created_at":"2026-07-28T10:00:00Z"}`,
		), nil
	})

	client, err := NewClient(Config{
		BaseURL:    "http://browser-use.test",
		Token:      "secret",
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(context.Background(), "session-42", "job-1", 90*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestClientReturnsTypedAPIError(t *testing.T) {
	t.Parallel()

	httpClient := testHTTPClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(
			http.StatusForbidden,
			`{"detail":"job belongs to another session"}`,
		), nil
	})

	client, err := NewClient(Config{
		BaseURL:    "http://browser-use.test",
		Token:      "secret",
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Cancel(context.Background(), "wrong-session", "job-1")
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("expected APIError, got %T: %v", err, err)
	}
	if apiError.StatusCode != http.StatusForbidden {
		t.Fatalf("unexpected status: %d", apiError.StatusCode)
	}
}

func TestNewClientRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := NewClient(Config{BaseURL: "file:///tmp/browser", Token: "secret"}); err == nil {
		t.Fatal("expected invalid scheme error")
	}
	if _, err := NewClient(Config{BaseURL: "http://localhost:8087"}); err == nil {
		t.Fatal("expected missing token error")
	}
}

func TestClientRejectsInvalidJobID(t *testing.T) {
	t.Parallel()

	client, err := NewClient(Config{BaseURL: "http://localhost:8087", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(context.Background(), "session-42", "../other-job", 0)
	if err == nil {
		t.Fatal("expected invalid job ID error")
	}
}
