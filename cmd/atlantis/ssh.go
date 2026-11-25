package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/t0mer/atlantis/internal/deco/tmp"
	"github.com/t0mer/atlantis/internal/deco/tmp/sshconn"
	"github.com/t0mer/atlantis/internal/deco/tmp/tdp"
	"github.com/t0mer/atlantis/internal/deco/tmpclient"
)

type sshProbeOptions struct {
	host, email, hostKey, username, dumpDir, knownHosts string
	port                                                int
	rawUsername, associate, shared                      bool
	dump, debug, skipTDP, acceptNew, typed              bool

	password    string
	in          io.Reader
	out, errOut io.Writer
}

func newSSHCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ssh", Short: "Reach a Deco on the LAN over SSH with the TP-Link ID, like the Deco app"}
	cmd.AddCommand(newSSHProbeCmd())
	cmd.AddCommand(newSSHSetWiFiCmd())
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
echo. The password is only sent to a Deco whose SSH host key is known: on
first contact the probe prints the key's fingerprint and stops. Check it,
then re-run with --accept-new-host-key (or --host-key SHA256:...) to trust
it; trusted keys are kept in ssh_known_hosts in the user config dir.`,
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
	f.BoolVar(&o.acceptNew, "accept-new-host-key", false, "trust and remember the host key on first contact")
	f.StringVar(&o.knownHosts, "known-hosts", "", "trusted host keys file (default: ssh_known_hosts in the user config dir)")
	f.StringVar(&o.username, "username", "", "SSH username override (default: derived from the email)")
	f.BoolVar(&o.rawUsername, "raw-username", false, "do not lowercase the email before use")
	f.BoolVar(&o.skipTDP, "no-tdp", false, "skip TDP discovery")
	f.BoolVar(&o.associate, "associate", false, "run the TMP version association step first (the app skips it; an M5 works either way)")
	f.BoolVar(&o.shared, "shared", false, "negotiate as a shared user instead of the owner")
	f.BoolVar(&o.dump, "dump", false, "write sanitised response fixtures")
	f.StringVar(&o.dumpDir, "dump-dir", "internal/deco/tmpclient/testdata", "directory for --dump fixtures")
	f.BoolVar(&o.debug, "debug", false, "print discovery and transport details (never secrets)")
	f.BoolVar(&o.typed, "typed", false, "print the parsed models instead of raw replies")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func runSSHProbe(ctx context.Context, o sshProbeOptions) error {
	c, err := sshSession(ctx, o)
	if err != nil {
		return err
	}
	defer func() {
		if err := c.Logout(ctx); err != nil {
			fmt.Fprintf(o.errOut, "disconnect: %v\n", err)
		}
	}()
	return readSession(ctx, c, sessionOutput{out: o.out, errOut: o.errOut, dump: o.dump, dumpDir: o.dumpDir, typed: o.typed})
}

// sshSession discovers the Deco, checks its host key and opens a logged-in
// session client. The caller must Logout.
func sshSession(ctx context.Context, o sshProbeOptions) (*tmpclient.Client, error) {
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

	hostID := fmt.Sprintf("%s:%d", o.host, port)
	khPath, err := knownHostsPath(o.knownHosts)
	if err != nil {
		return nil, err
	}
	pin := o.hostKey
	if pin == "" {
		pin = lookupHostKey(khPath, hostID)
	}
	if pin == "" && !o.acceptNew {
		// Learn the key without sending the password.
		_, err := sshconn.Dial(ctx, sshconn.Options{Host: o.host, Port: port, User: user, TPSBanner: tps,
			OnHostKey: func(fp string) { fmt.Fprintf(o.errOut, "SSH host key of %s: %s\n", hostID, fp) }})
		if errors.Is(err, sshconn.ErrUnknownHostKey) {
			return nil, errors.New("unknown SSH host key: check the fingerprint above, then re-run with --accept-new-host-key")
		}
		return nil, fmt.Errorf("reading the SSH host key of %s: %w", hostID, err)
	}

	if o.password == "" {
		if o.password, err = readSecret(o.in, bufio.NewReader(o.in), o.errOut, "TP-Link ID password: "); err != nil {
			return nil, err
		}
	}

	dial := func(ctx context.Context, _ func(string, ...any)) (io.ReadWriteCloser, error) {
		return sshconn.Dial(ctx, sshconn.Options{
			Host: o.host, Port: port, User: user, Password: o.password,
			HostKeyPin: pin, AcceptNew: pin == "" && o.acceptNew, TPSBanner: tps,
			OnHostKey: func(fp string) {
				fmt.Fprintf(o.errOut, "trusting new SSH host key of %s: %s\n", hostID, fp)
				if err := saveHostKey(khPath, hostID, fp); err != nil {
					fmt.Fprintf(o.errOut, "could not save the host key: %v\n", err)
				}
				pin = fp // a reconnect in this run must see the same key
			},
		})
	}
	c := tmpclient.New(dial, tmp.Options{
		Username: o.email, Owner: !o.shared, Heartbeat: 3 * time.Second, Associate: o.associate,
	})
	if err := c.Login(ctx); err != nil {
		if errors.Is(err, sshconn.ErrAuthFailed) {
			return nil, fmt.Errorf("%w\nCheck the password (and try --raw-username or --username) before retrying: the Deco locks out after repeated failures", err)
		}
		return nil, fmt.Errorf("connecting to %s over SSH: %w", o.host, err)
	}
	return c, nil
}

// knownHostsPath returns the trusted host keys file.
func knownHostsPath(p string) (string, error) {
	if p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("finding the config dir (use --known-hosts): %w", err)
	}
	return filepath.Join(dir, "atlantis", "ssh_known_hosts"), nil
}

// lookupHostKey returns the stored fingerprint for host:port, or "".
func lookupHostKey(path, hostID string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == hostID {
			return f[1]
		}
	}
	return ""
}

// saveHostKey appends host:port and its fingerprint (mode 0600).
func saveHostKey(path, hostID, fp string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", hostID, fp)
	return err
}

func newSSHSetWiFiCmd() *cobra.Command {
	var o sshProbeOptions
	var ssid string
	cmd := &cobra.Command{
		Use:   "set-wifi",
		Short: "Change the main Wi-Fi network's SSID and password on a Deco",
		Long: `Sets the main (host) network's SSID and password on every band, keeping
its other settings, and reads the result back. The new Wi-Fi password is read
from ATLANTIS_WIFI_PASSWORD, never from a flag. Every device on the main
network disconnects until it reconnects with the new details.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.email == "" {
				o.email = os.Getenv("ATLANTIS_CLOUD_EMAIL")
			}
			wifiPass := os.Getenv("ATLANTIS_WIFI_PASSWORD")
			if wifiPass == "" {
				return errors.New("set the new Wi-Fi password in ATLANTIS_WIFI_PASSWORD")
			}
			o.password = os.Getenv("ATLANTIS_CLOUD_PASSWORD")
			o.in, o.out, o.errOut = os.Stdin, os.Stdout, os.Stderr
			ctx := cmd.Context()
			c, err := sshSession(ctx, o)
			if err != nil {
				return err
			}
			defer func() { _ = c.Logout(ctx) }() // best effort; the change already happened or failed
			ch, err := c.SetHostWiFi(ctx, ssid, wifiPass)
			if err != nil {
				return err
			}
			fmt.Fprintf(o.out, "Main Wi-Fi is now %q on %s (confirmed by reading it back).\n", ssid, strings.Join(ch, ", "))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.host, "host", "", "Deco IP address on the LAN (required)")
	f.StringVar(&o.email, "email", "", "TP-Link ID email (or ATLANTIS_CLOUD_EMAIL)")
	f.StringVar(&o.hostKey, "host-key", "", "expected SSH host key fingerprint (SHA256:...)")
	f.StringVar(&o.knownHosts, "known-hosts", "", "trusted host keys file (default: ssh_known_hosts in the user config dir)")
	f.IntVar(&o.port, "port", 0, "SSH port (default: from TDP, else 20001)")
	f.StringVar(&ssid, "ssid", "", "new SSID (required)")
	f.BoolVar(&o.debug, "debug", false, "print discovery and transport details (never secrets)")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("ssid")
	return cmd
}
