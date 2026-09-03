// Package requests defines the ef requests Cobra command.
package requests

import (
	"io"

	"github.com/dlnilsson/excursion-funnel/cmd/loading"
	"github.com/dlnilsson/excursion-funnel/cmd/reportflags"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	apprequests "github.com/dlnilsson/excursion-funnel/internal/requests"
	"github.com/spf13/cobra"
)

type options struct {
	connection *reportflags.Flags
	limit      int
	since      string
	until      string
	all        bool
	json       bool
}

const loadingRequestsText = "Loading requests…"

// New creates the requests command and its today child command.
func New() *cobra.Command {
	opts := options{connection: reportflags.New(), limit: report.DefaultRecentToolCallLimit}
	cmd := &cobra.Command{Use: "requests", Short: "Inspect recorded web requests", Args: cobra.NoArgs}
	opts.connection.Bind(cmd.PersistentFlags())
	cmd.PersistentFlags().IntVar(&opts.limit, "limit", opts.limit, "maximum web requests to print")
	cmd.PersistentFlags().StringVar(&opts.since, "since", "", "start date, inclusive (YYYY-MM-DD)")
	cmd.PersistentFlags().StringVar(&opts.until, "until", "", "end date, inclusive (YYYY-MM-DD)")
	cmd.PersistentFlags().BoolVar(&opts.all, "all", false, "include requests recorded from all sources")
	cmd.PersistentFlags().BoolVar(&opts.json, "json", false, "print web requests as JSON")
	cmd.RunE = run(&opts, false)
	cmd.AddCommand(&cobra.Command{
		Use:   "today",
		Short: "Show web requests recorded during the current local day",
		Args:  cobra.NoArgs,
		RunE:  run(&opts, true),
	})
	return cmd
}

func run(opts *options, today bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		appOpts := apprequests.Options{
			Connection: opts.connection.Resolve(cmd.Flags()),
			Limit:      opts.limit,
			Since:      opts.since,
			Until:      opts.until,
			Source:     reportflags.CurrentSource(opts.all),
			JSON:       opts.json,
			Today:      today,
		}
		return loading.RunBuffered(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), appOpts.JSON,
			loadingRequestsText, func(out io.Writer) error {
				return apprequests.Run(cmd.Context(), out, appOpts)
			})
	}
}
