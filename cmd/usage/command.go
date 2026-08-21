// Package usage defines the ef usage Cobra command.
package usage

import (
	"io"

	"github.com/dlnilsson/excursion-funnel/cmd/reportflags"
	appusage "github.com/dlnilsson/excursion-funnel/internal/usage"
	"github.com/spf13/cobra"
)

type options struct {
	connection *reportflags.Flags
	since      string
	until      string
	groupBy    string
	directory  string
	branch     string
	json       bool
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
	cmd.PersistentFlags().BoolVar(&opts.json, "json", false, "print the summary as JSON")
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
				JSON:       opts.json,
				Today:      today,
			}
		)
		runUsage := func(writer io.Writer) error {
			return appusage.Run(ctx, writer, appOpts)
		}
		if !shouldAnimateUsage(appOpts.JSON, isTerminalWriter(out), isTerminalWriter(statusOut)) {
			return runUsage(out)
		}
		return runWithSpinner(ctx, out, statusOut, runUsage)
	}
}
