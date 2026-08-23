// Package sessions defines the ef sessions Cobra command.
package sessions

import (
	"io"

	"github.com/dlnilsson/excursion-funnel/cmd/loading"
	"github.com/dlnilsson/excursion-funnel/cmd/reportflags"
	appsessions "github.com/dlnilsson/excursion-funnel/internal/sessions"
	"github.com/spf13/cobra"
)

type options struct {
	connection *reportflags.Flags
	since      string
	until      string
	groupBy    string
	json       bool
}

// New creates the sessions command and its current-period shortcuts.
func New() *cobra.Command {
	opts := options{connection: reportflags.New(), groupBy: "day"}
	cmd := &cobra.Command{Use: "sessions", Short: "Summarize sessions started and used", Args: cobra.NoArgs}
	opts.connection.Bind(cmd.PersistentFlags())
	cmd.PersistentFlags().BoolVar(&opts.json, "json", false, "print the summary as JSON")
	cmd.Flags().StringVar(&opts.since, "since", "", "start date, inclusive (YYYY-MM-DD)")
	cmd.Flags().StringVar(&opts.until, "until", "", "end date, inclusive (YYYY-MM-DD)")
	cmd.Flags().StringVar(&opts.groupBy, "group-by", opts.groupBy, "grouping: day or week")
	cmd.RunE = run(&opts, "")
	cmd.AddCommand(
		&cobra.Command{Use: "today", Short: "Summarize sessions for the current local day", Args: cobra.NoArgs, RunE: run(&opts, "today")},
		&cobra.Command{Use: "week", Short: "Summarize sessions for the current local week", Args: cobra.NoArgs, RunE: run(&opts, "week")},
	)
	return cmd
}

func run(opts *options, shortcut string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		appOpts := appsessions.Options{
			Connection: opts.connection.Resolve(cmd.Flags()),
			Since:      opts.since,
			Until:      opts.until,
			GroupBy:    opts.groupBy,
			Shortcut:   shortcut,
			JSON:       opts.json,
		}
		return loading.RunBuffered(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), appOpts.JSON,
			"Loading sessions…", func(out io.Writer) error {
				return appsessions.Run(cmd.Context(), out, appOpts)
			})
	}
}
