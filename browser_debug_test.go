package agentize

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/browseruse"
	"github.com/gin-gonic/gin"
)

type debuggerBrowserService struct {
	screenshotSession string
	screenshotJob     string
}

func (*debuggerBrowserService) Health(context.Context) error { return nil }

func (*debuggerBrowserService) Start(
	context.Context,
	string,
	browseruse.StartJobRequest,
) (*browseruse.Job, error) {
	return nil, nil
}

func (*debuggerBrowserService) Get(
	context.Context,
	string,
	string,
	time.Duration,
) (*browseruse.Job, error) {
	return nil, nil
}

func (*debuggerBrowserService) Cancel(context.Context, string, string) (*browseruse.Job, error) {
	return nil, nil
}

func (*debuggerBrowserService) Debug(context.Context, int, int) (*browseruse.DebugSnapshot, error) {
	return &browseruse.DebugSnapshot{
		TotalJobs:         1,
		MaxJobs:           100,
		MaxConcurrentJobs: 2,
		Jobs: []browseruse.DebugJob{{
			Job: browseruse.Job{
				ID:                  "job-1",
				Status:              browseruse.JobSucceeded,
				CreatedAt:           time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC),
				ScreenshotAvailable: true,
			},
			SessionID: "session-1",
			Task:      "inspect example.com",
			LoadCount: 1,
			Loads: []browseruse.BrowserLoad{{
				Method: "GET",
				URL:    "https://example.com/app.js",
				Status: 200,
			}},
		}},
	}, nil
}

func (f *debuggerBrowserService) Screenshot(
	_ context.Context,
	sessionID, jobID string,
) (*browseruse.Screenshot, error) {
	f.screenshotSession = sessionID
	f.screenshotJob = jobID
	return &browseruse.Screenshot{
		Data:     []byte("PNG"),
		Name:     "browser-job-1.png",
		MIMEType: "image/png",
	}, nil
}

func TestBrowserDebugRoutesRenderLoadsAndProxyScreenshot(t *testing.T) {
	knowledge := createTestKnowledgeTree(t)
	service := &debuggerBrowserService{}
	ag, err := NewWithOptions(knowledge, &Options{BrowserUse: service})
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("AGENTIZE_DEBUG_UNSAFE", "1")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	ag.RegisterRoutes(router)

	page := httptest.NewRecorder()
	router.ServeHTTP(
		page,
		httptest.NewRequest(http.MethodGet, "/agentize/debug/browser", nil),
	)
	if page.Code != http.StatusOK {
		t.Fatalf("browser debug status=%d body=%s", page.Code, page.Body.String())
	}
	for _, want := range []string{
		"browser_use",
		"Ready",
		"inspect example.com",
		"https://example.com/app.js",
		"Open screenshot",
	} {
		if !strings.Contains(page.Body.String(), want) {
			t.Errorf("browser debug page missing %q", want)
		}
	}

	image := httptest.NewRecorder()
	router.ServeHTTP(
		image,
		httptest.NewRequest(
			http.MethodGet,
			"/agentize/debug/browser/job-1/screenshot?session_id=session-1",
			nil,
		),
	)
	if image.Code != http.StatusOK || image.Body.String() != "PNG" {
		t.Fatalf("browser screenshot status=%d body=%q", image.Code, image.Body.String())
	}
	if service.screenshotSession != "session-1" || service.screenshotJob != "job-1" {
		t.Fatalf("unexpected screenshot ownership: session=%q job=%q", service.screenshotSession, service.screenshotJob)
	}
}
