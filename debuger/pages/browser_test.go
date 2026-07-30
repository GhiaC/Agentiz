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
	successful := true
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
				Result: &browseruse.JobResult{
					Done:            true,
					Successful:      &successful,
					Steps:           3,
					DurationSeconds: 1.5,
					FinalResult:     `Found <b>the title</b>`,
					VisitedURLs:     []string{"https://example.com"},
					ActionNames:     []string{"navigate", "extract_text"},
					Actions:         []map[string]interface{}{{"name": "navigate"}},
				},
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
		"browser_use",
		"Ready",
		`"action":"run"`,
		"screenshot",
		"job-1",
		"session-1",
		"Open screenshot",
		"https://example.com/app.js",
		"text/javascript",
		"Network metadata only",
		"Auto-refresh",
		"Copy ID",
		"Run outcome",
		"Action trace",
		"42 B transferred",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered browser page missing %q", want)
		}
	}
	if strings.Contains(html, "<script>alert") || strings.Contains(html, "<unsafe>") || strings.Contains(html, "<b>the title</b>") {
		t.Fatalf("browser debug page rendered unescaped data:\n%s", html)
	}
}

func TestRenderBrowserDebugExplainsEmptyAndUnconfiguredStates(t *testing.T) {
	empty := RenderBrowserDebugWithStatus(&browseruse.DebugSnapshot{}, true, nil)
	for _, want := range []string{"browser_use", "Ready", "Only action", "sidecar restart"} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty browser page missing %q", want)
		}
	}

	unconfigured := RenderBrowserDebugWithStatus(nil, false, errors.New("browser-use is not configured"))
	for _, want := range []string{"browser_use", "Not configured", "UseBrowserUse"} {
		if !strings.Contains(unconfigured, want) {
			t.Errorf("unconfigured browser page missing %q", want)
		}
	}
}

func TestRenderBrowserDebugKeepsSidecarFailureInsidePage(t *testing.T) {
	html := RenderBrowserDebugWithStatus(nil, true, errors.New("connection refused"))
	if !strings.Contains(html, "Configured; debug unavailable") ||
		!strings.Contains(html, "Browser sidecar unavailable") ||
		!strings.Contains(html, "connection refused") {
		t.Fatalf("unexpected failure page:\n%s", html)
	}
}
