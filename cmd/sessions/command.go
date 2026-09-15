// Package sessions defines the ef sessions Cobra command.
package sessions

import (
	"github.com/dlnilsson/excursion-funnel/cmd/loading"
	"github.com/dlnilsson/excursion-funnel/cmd/reportflags"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	appsessions "github.com/dlnilsson/excursion-funnel/internal/sessions"
	"github.com/spf13/cobra"
)

const loadingSessionsText = "Loading sessions…"

type options struct {
	connection *reportflags.Flags
	since      string
	until      string
	groupBy    string
	all        bool
	json       bool
}

// New creates the sessions command and its current-period shortcuts.
func New() *cobra.Command {
	opts := options{connection: reportflags.New(), groupBy: "day"}
	cmd := &cobra.Command{Use: "sessions", Short: "Summarize sessions started and used", Args: cobra.NoArgs}
	opts.connection.Bind(cmd.PersistentFlags())
	cmd.PersistentFlags().BoolVar(&opts.all, "all", false, "include sessions recorded from all sources")
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
		var (
			ctx       = cmd.Context()
			out       = cmd.OutOrStdout()
			statusOut = cmd.ErrOrStderr()
			appOpts   = appsessions.Options{
				Connection: opts.connection.Resolve(cmd.Flags()),
				Since:      opts.since,
				Until:      opts.until,
				GroupBy:    opts.groupBy,
				Shortcut:   shortcut,
				Source:     reportflags.CurrentSource(opts.all),
				JSON:       opts.json,
			}
		)
		if !loading.ShouldAnimate(appOpts.JSON, loading.IsTerminalWriter(out), loading.IsTerminalWriter(statusOut)) {
			return appsessions.Run(ctx, out, appOpts)
		}
		rows, err := loading.Run(ctx, statusOut, loadingSessionsText, func() ([]report.SessionRow, error) {
			return appsessions.Load(ctx, appOpts)
		})
		if err != nil {
			return err
		}
		return appsessions.Render(out, rows, appOpts)
	}
}
