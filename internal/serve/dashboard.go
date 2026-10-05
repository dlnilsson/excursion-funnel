package serve

import (
	"net/http"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/dashboard"
)

func newHubDashboard(cfg config.Config) (http.Handler, error) {
	target, err := dashboard.URL(cfg)
	if err != nil {
		return nil, err
	}
	return http.RedirectHandler(target, http.StatusFound), nil
}
