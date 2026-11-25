package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn"
	"github.com/t0mer/atlantis/internal/deco/tmp/tdp"
	"github.com/t0mer/atlantis/internal/deco/tmpclient"
)

type sshProbeOptions struct {
	host, email, hostKey, username, dumpDir string
	port                                    int
	rawUsername, noAssociate, shared        bool
	dump, debug, skipTDP                    bool

	password    string
	in          io.Reader
	out, errOut io.Writer
}

func newSSHCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ssh", Short: "Reach a Deco on the LAN over SSH with the TP-Link ID, like the Deco app"}
	cmd.AddCommand(newSSHProbeCmd())
	return cmd
}

func newSSHProbeCmd() *cobra.Command {
	var o sshProbeOptions
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Read a Deco on the LAN through the app's SSH transport",
		Long: `Asks the Deco over TDP (UDP 20002) for its SSH port and username form,
signs in over SSH with your TP-Link ID (one attempt only), opens the TMP
channel, prints the read-only state as JSON (secrets redacted) and
disconnects. It never sends a write.

The password is read from ATLANTIS_CLOUD_PASSWORD or prompted for without
echo. On first contact the SSH host key fingerprint is printed; pass it back
with --host-key to pin it.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.email == "" {
				o.email = os.Getenv("ATLANTIS_CLOUD_EMAIL")
			}
			if o.email == "" && o.username == "" {
				return errors.New("no TP-Link ID: use --email or ATLANTIS_CLOUD_EMAIL")
			}
			o.password = os.Getenv("ATLANTIS_CLOUD_PASSWORD")
			o.in, o.out, o.errOut = os.Stdin, os.Stdout, os.Stderr
			return runSSHProbe(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.host, "host", "", "Deco IP address on the LAN (required)")
	f.StringVar(&o.email, "email", "", "TP-Link ID email (or ATLANTIS_CLOUD_EMAIL)")
	f.IntVar(&o.port, "port", 0, "SSH port (default: from TDP, else 20001)")
	f.StringVar(&o.hostKey, "host-key", "", "expected SSH host key fingerprint (SHA256:...)")
	f.StringVar(&o.username, "username", "", "SSH username override (default: derived from the email)")
	f.BoolVar(&o.rawUsername, "raw-username", false, "do not lowercase the email before use")
	f.BoolVar(&o.skipTDP, "no-tdp", false, "skip TDP discovery")
	f.BoolVar(&o.noAssociate, "no-associate", false, "skip the TMP version association step")
	f.BoolVar(&o.shared, "shared", false, "negotiate as a shared user instead of the owner")
	f.BoolVar(&o.dump, "dump", false, "write sanitised response fixtures")
	f.StringVar(&o.dumpDir, "dump-dir", "internal/deco/tmpclient/testdata", "directory for --dump fixtures")
	f.BoolVar(&o.debug, "debug", false, "print discovery and transport details (never secrets)")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func runSSHProbe(ctx context.Context, o sshProbeOptions) error {
	logf := func(string, ...any) {}
	if o.debug {
		logf = func(f string, a ...any) { fmt.Fprintf(o.errOut, "debug: "+f+"\n", a...) }
	}

	digest, tps, port := false, false, o.port
	if !o.skipTDP {
		dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		info, err := tdp.Discover(dctx, o.host)
		cancel()
		if err != nil {
			fmt.Fprintf(o.errOut, "TDP discovery failed (%v); using defaults\n", err)
		} else {
			digest, tps = info.NeedAccountDigest, info.TPSBanner()
			if port == 0 {
				port = info.SSHPort()
			}
			logf("TDP: model=%s role=%s tmp_port=%v need_account_digest=%v banner_type=%d/%d factory_default=%v",
				info.Model, info.Role, info.TMPPorts, info.NeedAccountDigest, info.BannerType, info.SSHBannerType, info.FactoryDefault)
		}
	}
	if port == 0 {
		port = 20001
	}
	user := o.username
	if user == "" {
		user = sshconn.Username(o.email, digest, o.rawUsername)
	}
	logf("SSH %s:%d, username %s, TPS banner %v", o.host, port, map[bool]string{true: "sha1(email)", false: "email"}[digest && o.username == ""], tps)

	if o.password == "" {
		var err error
		if o.password, err = readSecret(o.in, bufio.NewReader(o.in), o.errOut, "TP-Link ID password: "); err != nil {
			return err
		}
	}

	dial := func(ctx context.Context, _ func(string, ...any)) (io.ReadWriteCloser, error) {
		return sshconn.Dial(ctx, sshconn.Options{
			Host: o.host, Port: port, User: user, Password: o.password,
			HostKeyPin: o.hostKey, TPSBanner: tps,
			OnHostKey: func(fp string) {
				fmt.Fprintf(o.errOut, "SSH host key (pin with --host-key): %s\n", fp)
			},
		})
	}
	c := tmpclient.New(dial, tmp.Options{
		Username: o.email, Owner: !o.shared, Heartbeat: 3 * time.Second, Associate: !o.noAssociate,
	})
	if err := c.Login(ctx); err != nil {
		if errors.Is(err, sshconn.ErrAuthFailed) {
			return fmt.Errorf("%w\nCheck the password (and try --raw-username or --username) before retrying: the Deco locks out after repeated failures", err)
		}
		return fmt.Errorf("connecting to %s over SSH: %w", o.host, err)
	}
	defer func() {
		if err := c.Logout(ctx); err != nil {
			fmt.Fprintf(o.errOut, "disconnect: %v\n", err)
		}
	}()
	return readSession(ctx, c, sessionOutput{out: o.out, errOut: o.errOut, dump: o.dump, dumpDir: o.dumpDir})
}
