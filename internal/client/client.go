// Package client talks to Home Assistant over REST and WebSocket (ADR-0004).
package client

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"time"
)

// Config is everything needed to reach one Home Assistant instance.
type Config struct {
	// URL is the base URL of the instance, without `/api`.
	URL *url.URL
	// Token is a long-lived access token.
	Token string
	// Insecure skips TLS certificate verification.
	Insecure bool
	// Timeout is the per-request timeout.
	Timeout time.Duration
}

// HAClient holds both transports to one Home Assistant instance. Resources and data sources
// receive it from the provider's Configure.
type HAClient struct {
	REST *RESTClient
	WS   WSCommander
}

// New builds an HAClient. It does not contact Home Assistant: REST is stateless, and the
// WebSocket is dialled lazily on its first command.
func New(cfg Config) *HAClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.Insecure {
		// #nosec G402 -- opt-in via `insecure`
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in via `insecure`
	}
	httpClient := &http.Client{Transport: transport, Timeout: cfg.Timeout}

	return &HAClient{
		REST: &RESTClient{cfg: cfg, http: httpClient},
		WS:   newWSClient(cfg, httpClient),
	}
}

// RESTClient calls Home Assistant's `/api/*` endpoints.
type RESTClient struct {
	cfg  Config
	http *http.Client
}
