package main

import (
	"net"
	"net/http"

	"github.com/IceRhymers/databricks-claude/pkg/proxy"
	"github.com/IceRhymers/databricks-claude/pkg/tokencache"
)

// ProxyConfig holds the configuration for the proxy server.
type ProxyConfig struct {
	InferenceUpstream string
	OTELUpstream      string
	UCLogsTable       string
	TokenProvider     *tokencache.TokenProvider
	Verbose           bool
	APIKey            string
	TLSCertFile       string
	TLSKeyFile        string
}

// NewProxyServer returns an http.Handler that routes requests to the
// inference upstream (default) and the OTEL upstream (/otel/).
func NewProxyServer(config *ProxyConfig) http.Handler {
	return proxy.NewServer(&proxy.Config{
		InferenceUpstream: config.InferenceUpstream,
		OTELUpstream:      config.OTELUpstream,
		UCLogsTable:       config.UCLogsTable,
		TokenSource:       config.TokenProvider,
		Verbose:           config.Verbose,
		APIKey:            config.APIKey,
		TLSCertFile:       config.TLSCertFile,
		TLSKeyFile:        config.TLSKeyFile,
	})
}

// StartProxy binds to 127.0.0.1:0, starts serving, and returns the listener.
// Callers read l.Addr() to discover the assigned port.
// When certFile and keyFile are both non-empty, the listener serves TLS.
func StartProxy(handler http.Handler, certFile, keyFile string) (net.Listener, error) {
	return proxy.Start(handler, certFile, keyFile)
}
