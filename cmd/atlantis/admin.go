package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/secret"
)

func newKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Print a new ATLANTIS_SECRET_KEY",
		Long: `Prints a random 32-byte key (base64) for ATLANTIS_SECRET_KEY. Generate it
once and keep it: stored secrets cannot be read without it.`,
		Run: func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), secret.NewKey()) },
	}
}

// newHealthcheckCmd is for container health checks (no shell in the image).
func newHealthcheckCmd() *cobra.Command {
	var url string
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Exit 0 when the local server answers /health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if url == "" {
				listen := os.Getenv("ATLANTIS_SERVER_LISTEN")
				if listen == "" {
					listen = config.DefaultListen
				}
				_, port, err := net.SplitHostPort(listen)
				if err != nil {
					return err
				}
				url = "http://127.0.0.1:" + port + "/health"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("health: HTTP %d", resp.StatusCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "health URL (default: from ATLANTIS_SERVER_LISTEN, or :8080)")
	addRemovedFlags(cmd, "config")
	return cmd
}
