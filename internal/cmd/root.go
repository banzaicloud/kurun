package cmd

import "github.com/spf13/cobra"

func NewRootCommand() *cobra.Command {
	var params rootCommandParams

	cmd := &cobra.Command{
		Use: "kurun",
	}

	cmd.PersistentFlags().StringVar(&params.namespace, "namespace", "default", "namespace to use for resources")

	cmd.AddCommand(
		NewApplyCommand(),
		NewPortForwardCommand(&params),
		NewRunCommand(&params),
	)

	return cmd
}

type rootCommandParams struct {
	namespace string
}
