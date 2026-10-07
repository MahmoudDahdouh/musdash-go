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
func (s *Server) extraRoutes(handle func(pattern string, who access, h http.HandlerFunc), app, database, service resourceRoute) {
	s.readings = make(chan struct{}, maxReadings)
	s.terminals = make(chan struct{}, maxTerminals)
	s.pingEvery = terminalPing

	app("GET", "/metrics", member, s.appMetrics)
	app("GET", "/metrics/now", member, s.appMetricsNow)
	database("GET", "/metrics", member, s.databaseMetrics)
	database("GET", "/metrics/now", member, s.databaseMetricsNow)
	service("GET", "/metrics", member, s.serviceMetrics)
	service("GET", "/metrics/now", member, s.serviceMetricsNow)
	handle("GET /servers/{id}/metrics", member, s.serverMetrics)
	handle("GET /servers/{id}/metrics/now", member, s.serverMetricsNow)
	handle("POST /servers/{id}/metrics", admin, s.serverMetricsSave)

	app("GET", "/terminal", member, s.appTerminal)
	app("GET", "/terminal/ws", member, s.appTerminalWS)
	database("GET", "/terminal", member, s.databaseTerminal)
	database("GET", "/terminal/ws", member, s.databaseTerminalWS)
	service("GET", "/terminal", member, s.serviceTerminal)
	service("GET", "/terminal/ws", member, s.serviceTerminalWS)
}
