// Package open launches the web dashboard in the default browser.
package open

import (
	"fmt"
	"io"

	"github.com/dlnilsson/excursion-funnel/internal/config"
	"github.com/dlnilsson/excursion-funnel/internal/dashboard"
	"github.com/pkg/browser"
)

// Run opens the configured local or hub dashboard in the default browser.
func Run(cfg config.Config, out io.Writer) error {
	target, err := dashboard.URL(cfg)
	if err != nil {
		return err
	}
	if err := browser.OpenURL(target); err != nil {
		return fmt.Errorf("open dashboard in browser: %w; open %s manually", err, target)
	}
	_, err = fmt.Fprintf(out, "Opened %s\n", target)
	return err
}
