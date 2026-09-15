// Package usage defines the ef usage Cobra command.
package usage

import (
	"errors"

	"github.com/dlnilsson/excursion-funnel/cmd/loading"
	"github.com/dlnilsson/excursion-funnel/cmd/reportflags"
	"github.com/dlnilsson/excursion-funnel/internal/heatmap"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
	appusage "github.com/dlnilsson/excursion-funnel/internal/usage"
	"github.com/spf13/cobra"
)

const loadingUsageText = "Loading usage…"

// ErrHeatmapWithGroupBy reports --heatmap combined with an explicit --group-by,
// which would silently ignore the grouping.
// The message deliberately leads with a word rather than a flag name: Fang
// capitalizes the first letter of every rendered error, which would print
// "--Heatmap".
var ErrHeatmapWithGroupBy = errors.New("the calendar always groups by time; drop --group-by to use --heatmap")

type options struct {
	connection *reportflags.Flags
	since      string
	until      string
	groupBy    string
	directory  string
	branch     string
	all        bool
	json       bool
	heatmap    bool
}

// New creates the usage command and its today child command.
func New() *cobra.Command {
	opts := options{connection: reportflags.New(), groupBy: "model"}
	cmd := &cobra.Command{Use: "usage", Short: "Summarize recorded model usage", Args: cobra.NoArgs}
	opts.connection.Bind(cmd.PersistentFlags())
	cmd.PersistentFlags().StringVar(&opts.since, "since", "", "start date, inclusive (YYYY-MM-DD)")
	cmd.PersistentFlags().StringVar(&opts.until, "until", "", "end date, inclusive (YYYY-MM-DD)")
	cmd.PersistentFlags().StringVar(&opts.groupBy, "group-by", opts.groupBy, "grouping: model, provider, day, source, directory, or git_branch")
	cmd.PersistentFlags().StringVar(&opts.directory, "directory", "", "only requests from this working directory")
	cmd.PersistentFlags().StringVar(&opts.branch, "branch", "", "only requests from this git branch")
	cmd.PersistentFlags().BoolVar(&opts.all, "all", false, "include usage recorded from all sources")
	cmd.PersistentFlags().BoolVar(&opts.json, "json", false, "print the summary as JSON")
	cmd.PersistentFlags().BoolVar(&opts.heatmap, "heatmap", false, "render a token-activity calendar instead of the summary table")
	cmd.RunE = run(&opts, false)
	cmd.AddCommand(&cobra.Command{
		Use:   "today",
		Short: "Summarize usage for the current local day",
		Args:  cobra.NoArgs,
		RunE:  run(&opts, true),
	})
	return cmd
}

func run(opts *options, today bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		if opts.heatmap && cmd.Flags().Changed("group-by") {
			return ErrHeatmapWithGroupBy
		}
		var (
			ctx       = cmd.Context()
			out       = cmd.OutOrStdout()
			statusOut = cmd.ErrOrStderr()
			appOpts   = appusage.Options{
				Connection: opts.connection.Resolve(cmd.Flags()),
				Since:      opts.since,
				Until:      opts.until,
				GroupBy:    opts.groupBy,
				Directory:  opts.directory,
				Branch:     opts.branch,
				Source:     reportflags.CurrentSource(opts.all),
				JSON:       opts.json,
				Today:      today,
				Heatmap:    opts.heatmap,
			}
		)
		// Measure the real output stream: rendering may happen after the
		// loading animation, but never into it.
		appOpts.Width = reporting.TerminalWidth(out, heatmap.FullWidth, heatmap.FullWidth)

		if !loading.ShouldAnimate(appOpts.JSON, loading.IsTerminalWriter(out), loading.IsTerminalWriter(statusOut)) {
			return appusage.Run(ctx, out, appOpts)
		}
		data, err := loading.Run(ctx, statusOut, loadingUsageText, func() (appusage.Data, error) {
			return appusage.Load(ctx, appOpts)
		})
		if err != nil {
			return err
		}
		return appusage.Render(out, data, appOpts)
	}
}
