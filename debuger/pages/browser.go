package pages

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/ghiac/agentize/browseruse"
	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
)

const browserDebugPath = "/agentize/debug/browser"

// RenderBrowserDebug renders recent browser jobs and their bounded network-load
// metadata. The caller supplies fetchErr so a sidecar outage remains a useful
// debugger page instead of turning into an HTTP 500.
func RenderBrowserDebug(snapshot *browseruse.DebugSnapshot, fetchErr error) string {
	configured := snapshot != nil || fetchErr == nil
	return RenderBrowserDebugWithStatus(snapshot, configured, fetchErr)
}

// RenderBrowserDebugWithStatus renders the browser debugger with an explicit
// configuration status. Keeping configuration separate from connectivity lets
// operators distinguish missing wiring from an unavailable/old sidecar.
func RenderBrowserDebugWithStatus(
	snapshot *browseruse.DebugSnapshot,
	configured bool,
	fetchErr error,
) string {
	content := ui.ContainerStart()
	content += browserToolOverview(configured, fetchErr)
	content += `<div class="alert alert-info" role="alert">
			<strong>Network metadata only.</strong> Request/response bodies and headers are intentionally omitted.
			Screenshots are the latest viewport captured at a completed browser step.
		</div>`

	if fetchErr != nil {
		content += fmt.Sprintf(
			`<div class="alert alert-warning" role="alert"><strong>Browser sidecar unavailable:</strong> %s</div>`,
			template.HTMLEscapeString(fetchErr.Error()),
		)
		content += ui.ContainerEnd()
		return ui.Header("Agentize Debug - Browser") +
			ui.NavbarAndBody(browserDebugPath, content) + ui.Footer()
	}
	if snapshot == nil {
		content += components.InfoAlert("Browser-use is not configured.")
		content += ui.ContainerEnd()
		return ui.Header("Agentize Debug - Browser") +
			ui.NavbarAndBody(browserDebugPath, content) + ui.Footer()
	}

	content += browserStats(snapshot)
	content += `<div class="d-flex flex-wrap gap-2 align-items-center mb-3">
		<label for="browser-debug-filter" class="form-label mb-0 fw-semibold">Filter</label>
		<input id="browser-debug-filter" class="form-control form-control-sm" style="max-width:420px"
			placeholder="job, session, task, URL, MIME type…" oninput="filterBrowserDebug(this.value)">
	</div>`

	if len(snapshot.Jobs) == 0 {
		content += components.InfoAlert(
			`No browser jobs have been recorded yet. Invoke browser_use with action "run" first. ` +
				`Jobs live in the sidecar process and disappear after its retention TTL or a sidecar restart.`,
		)
	} else {
		for _, job := range snapshot.Jobs {
			content += browserJobCard(&job)
		}
	}
	content += `<script>
	function filterBrowserDebug(value) {
		var query = (value || '').toLowerCase().trim();
		document.querySelectorAll('[data-browser-debug-job]').forEach(function (node) {
			node.style.display = !query || node.dataset.browserSearch.indexOf(query) !== -1 ? '' : 'none';
		});
	}
	</script>`
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Browser") +
		ui.NavbarAndBody(browserDebugPath, content) + ui.Footer()
}

func browserToolOverview(configured bool, fetchErr error) string {
	status := `<span class="badge text-bg-secondary">Not configured</span>`
	statusDetail := `Create a browseruse.Client and pass it through agentize.Options{BrowserUse: client} ` +
		`or call ag.UseBrowserUse(client).`
	if configured && fetchErr != nil {
		status = `<span class="badge text-bg-warning">Configured; debug unavailable</span>`
		statusDetail = `The browser_use schema is wired, but Agentize could not read debug metadata. ` +
			`Rebuild/restart the sidecar so it exposes GET /v1/debug/jobs.`
	} else if configured {
		status = `<span class="badge text-bg-success">Ready</span>`
		statusDetail = `The tool and browser debug endpoint are connected. Only action "run" creates a browser job.`
	}

	return fmt.Sprintf(`<section class="card mb-3">
		<div class="card-header d-flex flex-wrap gap-2 justify-content-between align-items-center">
			<div><strong>Tool:</strong> <code>browser_use</code></div>
			%s
		</div>
		<div class="card-body">
			<p class="mb-3">%s</p>
			<div class="row g-3">
				<div class="col-lg-5">
					<div class="small text-muted mb-1">Supported actions</div>
					<div class="d-flex flex-wrap gap-2">
						<code>run</code><code>status</code><code>screenshot</code><code>cancel</code>
					</div>
				</div>
				<div class="col-lg-7">
					<div class="small text-muted mb-1">Create debug data</div>
					<pre class="bg-light border rounded p-2 mb-2"><code>{"action":"run","task":"Open example.com and report the page title"}</code></pre>
					<div class="small text-muted mb-1">Capture the latest viewport</div>
					<pre class="bg-light border rounded p-2 mb-0"><code>{"action":"screenshot","job_id":"&lt;job_id from run&gt;"}</code></pre>
				</div>
			</div>
			<div class="small text-muted mt-3">
				Actual invocations also appear under <a href="/agentize/debug/tool-calls">Tool Calls</a>
				after the LLM calls the tool and the call is persisted. If tool approvals are enabled,
				approve the <code>run</code> call under <a href="/agentize/debug/reviews">Reviews</a>
				before expecting a browser job here.
			</div>
		</div>
	</section>`, status, template.HTMLEscapeString(statusDetail))
}

func browserStats(snapshot *browseruse.DebugSnapshot) string {
	return fmt.Sprintf(`<div class="row g-3 mb-3">
		<div class="col-6 col-lg-3"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Jobs retained</div><div class="fs-4 fw-semibold">%d / %d</div>
		</div></div></div>
		<div class="col-6 col-lg-3"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Active jobs</div><div class="fs-4 fw-semibold">%d</div>
		</div></div></div>
		<div class="col-6 col-lg-3"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Concurrency</div><div class="fs-4 fw-semibold">%d</div>
		</div></div></div>
		<div class="col-6 col-lg-3"><div class="card h-100"><div class="card-body">
			<div class="text-muted small">Loads shown</div><div class="fs-4 fw-semibold">%d</div>
		</div></div></div>
	</div>`,
		snapshot.TotalJobs,
		snapshot.MaxJobs,
		snapshot.RunningJobs,
		snapshot.MaxConcurrentJobs,
		countBrowserLoads(snapshot.Jobs),
	)
}

func browserJobCard(job *browseruse.DebugJob) string {
	searchParts := []string{job.ID, job.SessionID, job.Task, string(job.Status), job.Error}
	for _, load := range job.Loads {
		searchParts = append(searchParts, load.URL, load.MIMEType, load.Method)
	}
	search := template.HTMLEscapeString(strings.ToLower(strings.Join(searchParts, " ")))
	started := job.CreatedAt
	if job.StartedAt != nil {
		started = *job.StartedAt
	}
	duration := "—"
	if job.CompletedAt != nil {
		duration = debuger.FormatDurationMs(job.CompletedAt.Sub(started).Milliseconds())
	} else if job.Status == browseruse.JobRunning {
		duration = debuger.FormatDurationMs(time.Since(started).Milliseconds())
	}

	screenshot := `<span class="text-muted">Not available</span>`
	if job.ScreenshotAvailable {
		screenshotURL := browserDebugPath + "/" + template.URLQueryEscaper(job.ID) +
			"/screenshot?session_id=" + url.QueryEscape(job.SessionID)
		screenshot = fmt.Sprintf(
			`<a href="%s" target="_blank" class="btn btn-sm btn-outline-primary">Open screenshot</a>`,
			template.HTMLEscapeString(screenshotURL),
		)
	}

	errorBlock := ""
	if job.Error != "" {
		errorBlock = fmt.Sprintf(
			`<div class="alert alert-danger py-2 mt-3 mb-0"><strong>Error:</strong> %s</div>`,
			template.HTMLEscapeString(job.Error),
		)
	}

	return fmt.Sprintf(`<section class="card mb-3" data-browser-debug-job data-browser-search="%s">
		<div class="card-header d-flex flex-wrap gap-2 justify-content-between align-items-center">
			<div><strong>%s</strong> %s</div>
			<div class="d-flex gap-2 align-items-center">%s</div>
		</div>
		<div class="card-body">
			<div class="row g-3">
				<div class="col-lg-7">
					<div class="small text-muted mb-1">Task</div>
					<div class="text-break">%s</div>
				</div>
				<div class="col-lg-5">
					<table class="table table-sm table-borderless mb-0">
						<tr><th>Session</th><td class="text-break">%s</td></tr>
						<tr><th>Created</th><td>%s</td></tr>
						<tr><th>Duration</th><td>%s</td></tr>
						<tr><th>Network loads</th><td>%d</td></tr>
					</table>
				</div>
			</div>
			%s
			%s
		</div>
	</section>`,
		search,
		template.HTMLEscapeString(job.ID),
		browserStatusBadge(job.Status),
		screenshot,
		template.HTMLEscapeString(job.Task),
		template.HTMLEscapeString(job.SessionID),
		debuger.FormatTime(job.CreatedAt),
		duration,
		job.LoadCount,
		errorBlock,
		browserLoadsTable(job),
	)
}

func browserLoadsTable(job *browseruse.DebugJob) string {
	if len(job.Loads) == 0 {
		return `<div class="text-muted small mt-3">No completed network metadata is available yet.</div>`
	}
	hidden := job.LoadCount - len(job.Loads)
	summary := fmt.Sprintf("%d most recent loads", len(job.Loads))
	if hidden > 0 {
		summary += fmt.Sprintf(" (%d older omitted)", hidden)
	}
	out := fmt.Sprintf(`<details class="mt-3"><summary class="fw-semibold">%s</summary>
		<div class="table-responsive mt-2"><table class="table table-sm table-hover align-middle">
		<thead><tr><th>Time</th><th>Method</th><th>Status</th><th>Type</th><th>Size</th><th>Duration</th><th>URL</th></tr></thead><tbody>`,
		template.HTMLEscapeString(summary),
	)
	for _, load := range job.Loads {
		when := "—"
		if load.StartedAt != nil {
			when = debuger.FormatTime(*load.StartedAt)
		}
		status := fmt.Sprintf("%d", load.Status)
		statusClass := "secondary"
		switch {
		case load.Failed || load.Status == 0 || load.Status >= 500:
			statusClass = "danger"
		case load.Status >= 400:
			statusClass = "warning text-dark"
		case load.Status >= 300:
			statusClass = "info text-dark"
		case load.Status >= 200:
			statusClass = "success"
		}
		out += fmt.Sprintf(`<tr>
			<td class="text-nowrap">%s</td>
			<td>%s</td>
			<td>%s</td>
			<td class="text-break">%s</td>
			<td class="text-nowrap">%s</td>
			<td class="text-nowrap">%s</td>
			<td class="text-break" style="min-width:320px">%s</td>
		</tr>`,
			when,
			components.InlineCode(template.HTMLEscapeString(load.Method)),
			components.Badge(status, statusClass),
			template.HTMLEscapeString(load.MIMEType),
			formatBytes(load.Bytes),
			debuger.FormatDurationMs(int64(load.DurationMs)),
			template.HTMLEscapeString(load.URL),
		)
	}
	return out + `</tbody></table></div></details>`
}

func browserStatusBadge(status browseruse.JobStatus) string {
	switch status {
	case browseruse.JobSucceeded:
		return components.Badge("succeeded", "success")
	case browseruse.JobFailed:
		return components.Badge("failed", "danger")
	case browseruse.JobCancelled:
		return components.Badge("cancelled", "secondary")
	case browseruse.JobRunning:
		return components.Badge("running", "primary")
	default:
		return components.Badge(string(status), "warning text-dark")
	}
}

func countBrowserLoads(jobs []browseruse.DebugJob) int {
	total := 0
	for _, job := range jobs {
		total += len(job.Loads)
	}
	return total
}
