package metrics

import (
	"net"
	"net/http"
	"time"
)

// StartServer starts a dedicated, unauthenticated HTTP server that exposes the
// Agentize Prometheus metrics on addr (e.g. ":9091"), separate from the main
// application/admin server. The metrics handler is served at both "/metrics"
// (the Prometheus convention) and "/agentize/metrics" (so an existing scrape
// config only needs its target port changed, not its path).
//
// This endpoint has NO authentication by design: Prometheus cannot perform the
// interactive admin login the main /agentize pages require, so the metrics must
// live off that gate. Bind it to an internal interface or restrict it at the
// network layer so only the scraper can reach it.
//
// The TCP port is bound synchronously, so a bind failure (e.g. address in use)
// is returned immediately; requests are then served from a background
// goroutine. The returned *http.Server lets the caller Shutdown it; callers
// that want a process-lifetime endpoint may ignore it.
func StartServer(addr string) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	h := metricsHandler()
	mux := http.NewServeMux()
	mux.Handle("/metrics", h)
	mux.Handle("/agentize/metrics", h)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second, // bound slow-header clients (Slowloris)
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
