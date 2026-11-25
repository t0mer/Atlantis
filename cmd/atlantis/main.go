// Command atlantis monitors and manages a TP-Link Deco mesh over its local API.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:           "atlantis",
		Short:         "Monitor and manage a TP-Link Deco mesh from the LAN",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newProbeCmd())
	root.AddCommand(newCloudCmd())
	root.AddCommand(newSSHCmd())
	root.AddCommand(newServeCmd())
	root.AddCommand(newKeygenCmd())
	root.AddCommand(newHealthcheckCmd())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the build version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	})
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
