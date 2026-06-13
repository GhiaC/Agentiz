package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDedicatedRegistryExposesAgentizeMetrics verifies A3/A6: the default
// handler serves the dedicated registry, including the new store/audit/summary
// collectors and the Go runtime collector.
func TestDedicatedRegistryExposesAgentizeMetrics(t *testing.T) {
	// Emit one sample of each new metric so its series appears in the output.
	AuditAction("delete_user_data", "ok")
	StoreQuery("Get", "sqlite", 5*time.Millisecond)
	StoreQuery("", "", time.Millisecond) // defaults to operation/backend "unknown"
	SummaryAge(120 * time.Second)
	SummaryAge(0) // skipped (no observation), must not panic

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Result().Body)
	out := string(body)

	for _, want := range []string{
		"agentize_audit_actions_total",
		"agentize_store_query_duration_seconds",
		"agentize_summary_age_seconds",
		"go_goroutines", // runtime collector present on the dedicated registry
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output is missing %q", want)
		}
	}
}

func TestStatusHelper(t *testing.T) {
	if Status(nil) != "ok" {
		t.Error("Status(nil) should be ok")
	}
	if Status(io.EOF) != "error" {
		t.Error("Status(err) should be error")
	}
}
