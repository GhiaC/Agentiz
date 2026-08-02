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

func TestClientScreenshotUsesTrustedSessionAndReturnsImage(t *testing.T) {
	t.Parallel()

	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/jobs/job-1/screenshot" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get(sessionHeader); got != "session-42" {
			t.Fatalf("unexpected session header: %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(bytes.NewReader([]byte("PNG"))),
		}, nil
	})
	client, err := NewClient(Config{
		BaseURL:    "http://browser-use.test",
		Token:      "secret",
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	screenshot, err := client.Screenshot(context.Background(), "session-42", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(screenshot.Data) != "PNG" || screenshot.MIMEType != "image/png" {
		t.Fatalf("unexpected screenshot: %#v", screenshot)
	}
}

func TestClientListsAndDownloadsJobFiles(t *testing.T) {
	t.Parallel()

	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get(sessionHeader); got != "session-42" {
			t.Fatalf("unexpected session header: %q", got)
		}
		switch request.URL.Path {
		case "/v1/jobs/job-1/downloads":
			return jsonResponse(http.StatusOK, `{"files":[{"name":"report.csv","mime_type":"text/csv","size":5}]}`), nil
		case "/v1/jobs/job-1/downloads/report.csv":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/csv; charset=utf-8"}},
				Body:       io.NopCloser(bytes.NewReader([]byte("a,b\n1"))),
			}, nil
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
			return nil, nil
		}
	})
	client, err := NewClient(Config{BaseURL: "http://browser-use.test", Token: "secret", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	files, err := client.Downloads(context.Background(), "session-42", "job-1")
	if err != nil || len(files) != 1 || files[0].Name != "report.csv" {
		t.Fatalf("unexpected downloads: %#v, %v", files, err)
	}
	download, err := client.Download(context.Background(), "session-42", "job-1", "report.csv")
	if err != nil {
		t.Fatal(err)
	}
	if string(download.Data) != "a,b\n1" || download.MIMEType != "text/csv" || download.Size != 5 {
		t.Fatalf("unexpected download: %#v", download)
	}
}

func TestClientListsAndClosesPersistentTabs(t *testing.T) {
	t.Parallel()

	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get(sessionHeader); got != "session-42" {
			t.Fatalf("unexpected session header: %q", got)
		}
		switch request.URL.Path {
		case "/v1/tabs":
			if request.Method != http.MethodGet {
				t.Fatalf("unexpected tabs method: %s", request.Method)
			}
			return jsonResponse(http.StatusOK, `{"tabs":[{"id":"tab-1","url":"https://example.com","active":true}]}`), nil
		case "/v1/tabs/tab-1/close":
			if request.Method != http.MethodPost {
				t.Fatalf("unexpected close method: %s", request.Method)
			}
			return jsonResponse(http.StatusOK, `{"tabs":[]}`), nil
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
			return nil, nil
		}
	})
	client, err := NewClient(Config{BaseURL: "http://browser-use.test", Token: "secret", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	tabs, err := client.Tabs(context.Background(), "session-42")
	if err != nil || len(tabs) != 1 || tabs[0].ID != "tab-1" || !tabs[0].Active {
		t.Fatalf("unexpected tabs: %#v, %v", tabs, err)
	}
	remaining, err := client.CloseTab(context.Background(), "session-42", "tab-1")
	if err != nil || len(remaining) != 0 {
		t.Fatalf("unexpected remaining tabs: %#v, %v", remaining, err)
	}
}

func TestClientDebugRequestsBoundedMetadata(t *testing.T) {
	t.Parallel()

	httpClient := testHTTPClient(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/debug/jobs" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		if request.URL.Query().Get("limit") != "100" || request.URL.Query().Get("load_limit") != "250" {
			t.Fatalf("unexpected query: %s", request.URL.RawQuery)
		}
		return jsonResponse(http.StatusOK, `{
			"total_jobs":1,
			"running_jobs":0,
			"max_jobs":1000,
			"max_concurrent_jobs":2,
			"jobs":[{
				"id":"job-1",
				"status":"succeeded",
				"created_at":"2026-07-30T10:00:00Z",
				"session_id":"session-42",
				"task":"inspect",
				"load_count":1,
				"loads":[{"method":"GET","url":"https://example.com","status":200,"duration_ms":10,"bytes":42,"failed":false}]
			}]
		}`), nil
	})
	client, err := NewClient(Config{
		BaseURL:    "http://browser-use.test",
		Token:      "secret",
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Debug(context.Background(), 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TotalJobs != 1 || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].LoadCount != 1 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
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
	_, err = client.CloseTab(context.Background(), "session-42", "../other-tab")
	if err == nil {
		t.Fatal("expected invalid tab ID error")
	}
}

func TestClientRejectsUnsafeDownloadName(t *testing.T) {
	t.Parallel()
	client, err := NewClient(Config{BaseURL: "http://localhost:8087", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Download(context.Background(), "session-42", "job-1", "../secret"); err == nil {
		t.Fatal("expected invalid download name error")
	}
}
