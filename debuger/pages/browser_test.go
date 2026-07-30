package pages

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/browseruse"
)

func TestRenderBrowserDebugShowsJobsLoadsAndScreenshot(t *testing.T) {
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	finished := now.Add(2 * time.Second)
	snapshot := &browseruse.DebugSnapshot{
		TotalJobs:         1,
		MaxJobs:           1000,
		MaxConcurrentJobs: 2,
		Jobs: []browseruse.DebugJob{{
			Job: browseruse.Job{
				ID:                  "job-1",
				Status:              browseruse.JobSucceeded,
				CreatedAt:           now,
				CompletedAt:         &finished,
				ScreenshotAvailable: true,
			},
			SessionID: "session-1",
			Task:      `inspect <script>alert("x")</script>`,
			LoadCount: 1,
			Loads: []browseruse.BrowserLoad{{
				StartedAt:  &now,
				DurationMs: 12,
				Method:     "GET",
				URL:        "https://example.com/app.js?x=<unsafe>",
				Status:     200,
				MIMEType:   "text/javascript",
				Bytes:      42,
			}},
		}},
	}

	html := RenderBrowserDebug(snapshot, nil)
	for _, want := range []string{
		"Browser",
		"job-1",
		"session-1",
		"Open screenshot",
		"https://example.com/app.js",
		"text/javascript",
		"Network metadata only",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered browser page missing %q", want)
		}
	}
	if strings.Contains(html, "<script>alert") || strings.Contains(html, "<unsafe>") {
		t.Fatalf("browser debug page rendered unescaped data:\n%s", html)
	}
}

func TestRenderBrowserDebugKeepsSidecarFailureInsidePage(t *testing.T) {
	html := RenderBrowserDebug(nil, errors.New("connection refused"))
	if !strings.Contains(html, "Browser sidecar unavailable") || !strings.Contains(html, "connection refused") {
		t.Fatalf("unexpected failure page:\n%s", html)
	}
}
