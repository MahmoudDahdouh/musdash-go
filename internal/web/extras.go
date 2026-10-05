package web

import "net/http"

// extras is what the Server holds for metrics and terminals.
type extras struct {
	// readings bounds the usage readings in progress at once.
	readings chan struct{}
	// terminals bounds the terminals open at once.
	terminals chan struct{}
}

// extraRoutes registers the routes of metrics and terminals.
func (s *Server) extraRoutes(mux *http.ServeMux) {
	s.readings = make(chan struct{}, maxReadings)
	s.terminals = make(chan struct{}, maxTerminals)

	mux.Handle("GET /apps/{id}/metrics", s.authed(s.appMetrics))
	mux.Handle("GET /apps/{id}/metrics/now", s.authed(s.appMetricsNow))
	mux.Handle("GET /databases/{id}/metrics", s.authed(s.databaseMetrics))
	mux.Handle("GET /databases/{id}/metrics/now", s.authed(s.databaseMetricsNow))
	mux.Handle("GET /services/{id}/metrics", s.authed(s.serviceMetrics))
	mux.Handle("GET /services/{id}/metrics/now", s.authed(s.serviceMetricsNow))
	mux.Handle("GET /servers/{id}/metrics", s.authed(s.serverMetrics))
	mux.Handle("GET /servers/{id}/metrics/now", s.authed(s.serverMetricsNow))
	mux.Handle("POST /servers/{id}/metrics", s.authed(s.serverMetricsSave))

	mux.Handle("GET /apps/{id}/terminal", s.authed(s.appTerminal))
	mux.Handle("GET /apps/{id}/terminal/ws", s.authed(s.appTerminalWS))
	mux.Handle("GET /databases/{id}/terminal", s.authed(s.databaseTerminal))
	mux.Handle("GET /databases/{id}/terminal/ws", s.authed(s.databaseTerminalWS))
	mux.Handle("GET /services/{id}/terminal", s.authed(s.serviceTerminal))
	mux.Handle("GET /services/{id}/terminal/ws", s.authed(s.serviceTerminalWS))
}
