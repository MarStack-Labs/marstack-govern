package cli

import (
	"fmt"

	"github.com/marstack-labs/marstack-govern/internal/version"
	"github.com/spf13/cobra"
)

func Execute() error {
	return newRootCommand().Execute()
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "margov",
		Short:         "Multi-tenancy and governance platform for Kubernetes",
		Long:          "margov runs the marstack-govern control plane and the client that talks to it.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
	}

	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(newVersionCommand())

	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and build commit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return err
		},
	}
}
