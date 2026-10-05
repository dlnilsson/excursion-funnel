// Package dashboard resolves the local or hub web dashboard URL.
package dashboard

import (
	"fmt"
	"net"
	"net/url"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/hubauth"
)

// URL returns the browser URL for the configured dashboard. Hub dashboards
// listen on port 8788, separately from the usage gateway.
func URL(cfg config.Config) (string, error) {
	if cfg.HubAddr != "" {
		target, err := url.Parse(hubauth.GatewayURL(cfg.HubAddr, cfg.HubInsecure))
		if err != nil {
			return "", fmt.Errorf("hub dashboard address: %w", err)
		}
		host, _, err := net.SplitHostPort(target.Host)
		if err != nil {
			return "", fmt.Errorf("hub dashboard address: %w", err)
		}
		target.Host = net.JoinHostPort(host, "8788")
		target.Path = "/ui/"
		return target.String(), nil
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return "", fmt.Errorf("dashboard address: %w", err)
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	} else if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "::1"
	}
	target := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/ui/"}
	return target.String(), nil
}
