package serve

import (
	"net/http"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/hubauth"
)

func newHubDashboard(cfg config.Config) http.Handler {
	return http.RedirectHandler(hubauth.GatewayURL(cfg.HubAddr, cfg.HubInsecure)+"/ui/", http.StatusFound)
}
