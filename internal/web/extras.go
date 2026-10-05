package web

import (
	"net/http"
	"time"
)

// extras is what the Server holds for metrics and terminals.
type extras struct {
	// readings bounds the usage readings in progress at once.
	readings chan struct{}
	// terminals bounds the terminals open at once.
	terminals chan struct{}
	// pingEvery is how often a terminal's connection is pinged. Tests
	// shorten it.
	pingEvery time.Duration
}

// extraRoutes registers the routes of metrics and terminals. What a
// Member works with (apps, databases, services) a Member may look at and
// open a terminal in; whether a server is sampled is the team's to decide.
func (s *Server) extraRoutes(handle func(pattern string, who access, h http.HandlerFunc)) {
	s.readings = make(chan struct{}, maxReadings)
	s.terminals = make(chan struct{}, maxTerminals)
	s.pingEvery = terminalPing

	handle("GET /apps/{id}/metrics", member, s.appMetrics)
	handle("GET /apps/{id}/metrics/now", member, s.appMetricsNow)
	handle("GET /databases/{id}/metrics", member, s.databaseMetrics)
	handle("GET /databases/{id}/metrics/now", member, s.databaseMetricsNow)
	handle("GET /services/{id}/metrics", member, s.serviceMetrics)
	handle("GET /services/{id}/metrics/now", member, s.serviceMetricsNow)
	handle("GET /servers/{id}/metrics", member, s.serverMetrics)
	handle("GET /servers/{id}/metrics/now", member, s.serverMetricsNow)
	handle("POST /servers/{id}/metrics", admin, s.serverMetricsSave)

	handle("GET /apps/{id}/terminal", member, s.appTerminal)
	handle("GET /apps/{id}/terminal/ws", member, s.appTerminalWS)
	handle("GET /databases/{id}/terminal", member, s.databaseTerminal)
	handle("GET /databases/{id}/terminal/ws", member, s.databaseTerminalWS)
	handle("GET /services/{id}/terminal", member, s.serviceTerminal)
	handle("GET /services/{id}/terminal/ws", member, s.serviceTerminalWS)
}
