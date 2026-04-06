package main

import (
	"net"
	"net/http"

	"github.com/IceRhymers/databricks-codex/pkg/proxy"
	"github.com/IceRhymers/databricks-codex/pkg/tokencache"
)

// ProxyConfig holds the configuration for the proxy server.
type ProxyConfig struct {
	InferenceUpstream string
	OTELUpstream      string
	UCMetricsTable    string
	UCLogsTable       string
	TokenProvider     *tokencache.TokenProvider
	Verbose           bool
}

// NewProxyServer returns an http.Handler that routes requests to the
// inference upstream (default) and the OTEL upstream (/otel/).
func NewProxyServer(config *ProxyConfig) http.Handler {
	return proxy.NewServer(&proxy.Config{
		InferenceUpstream: config.InferenceUpstream,
		OTELUpstream:      config.OTELUpstream,
		UCMetricsTable:    config.UCMetricsTable,
		UCLogsTable:       config.UCLogsTable,
		TokenSource:       config.TokenProvider,
		Verbose:           config.Verbose,
	})
}

// StartProxy binds to 127.0.0.1:0, starts serving, and returns the listener.
// Callers read l.Addr() to discover the assigned port.
func StartProxy(handler http.Handler) (net.Listener, error) {
	return proxy.Start(handler)
}
