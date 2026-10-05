package dashboard

import (
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/config"
)

func TestURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"default", config.Default(), "http://127.0.0.1:8787/ui/"},
		{"custom local", config.Config{Addr: "localhost:8888"}, "http://localhost:8888/ui/"},
		{"all interfaces", config.Config{Addr: "0.0.0.0:8787"}, "http://127.0.0.1:8787/ui/"},
		{"empty host", config.Config{Addr: ":8787"}, "http://127.0.0.1:8787/ui/"},
		{"ipv6", config.Config{Addr: "[::1]:8787"}, "http://[::1]:8787/ui/"},
		{"all ipv6 interfaces", config.Config{Addr: "[::]:8787"}, "http://[::1]:8787/ui/"},
		{"hub", config.Config{HubAddr: "homebox.tail588fb8.ts.net:9494"}, "https://homebox.tail588fb8.ts.net:8788/ui/"},
		{"insecure hub", config.Config{HubAddr: "hub.example.test:9494", HubInsecure: true}, "http://hub.example.test:8788/ui/"},
		{"quack URI", config.Config{HubAddr: "quack://hub.example.test:9494"}, "https://hub.example.test:8788/ui/"},
		{"quack address", config.Config{HubAddr: " quack:hub.example.test:9494 "}, "https://hub.example.test:8788/ui/"},
		{"ipv6 hub", config.Config{HubAddr: "[::1]:9494"}, "https://[::1]:8788/ui/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := URL(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("URL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestURLRejectsInvalidAddresses(t *testing.T) {
	for _, cfg := range []config.Config{{Addr: "invalid"}, {HubAddr: "invalid"}, {HubAddr: "[broken:9494"}} {
		if _, err := URL(cfg); err == nil {
			t.Fatalf("URL(%+v) succeeded, want invalid address error", cfg)
		}
	}
}
