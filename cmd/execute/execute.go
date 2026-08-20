// Package execute applies Fang's terminal experience to the ef Cobra tree.
package execute

import (
	"context"
	"errors"
	"io"
	"os"

	"charm.land/fang/v2"
	"github.com/dlnilsson/excursion-funnel/internal/safelog"
	"github.com/dlnilsson/excursion-funnel/internal/version"
	"github.com/spf13/cobra"
)

// Execute runs root with Fang's help, error, version, completion, manpage, and
// optional signal handling. Fang renders errors; callers only choose the exit
// status from the returned error.
func Execute(ctx context.Context, root *cobra.Command, signals ...os.Signal) error {
	options := []fang.Option{
		fang.WithVersion(version.Current().Version),
		fang.WithErrorHandler(redactedErrorHandler),
	}
	if len(signals) > 0 {
		options = append(options, fang.WithNotifySignal(signals...))
	}
	return fang.Execute(ctx, root, options...)
}

func redactedErrorHandler(out io.Writer, styles fang.Styles, err error) {
	message := safelog.Redact(err.Error())
	if message != err.Error() {
		// Fang title-cases the first word in styled errors. Keep the redaction
		// marker after that word so its conventional spelling remains intact.
		message = "details: " + message
	}
	redacted := errors.New(message)
	fang.DefaultErrorHandler(out, styles, redacted)
}
