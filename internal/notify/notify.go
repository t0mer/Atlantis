// Package notify delivers events to notification channels: Shoutrrr URLs
// (Telegram, Slack, Pushover, ...) and WhatsApp through OpenWA. Delivery is
// best-effort: a slow or failing channel never blocks the poller.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/containrrr/shoutrrr"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/store"
)

// Channel kinds.
const (
	KindShoutrrr = "shoutrrr"
	KindOpenWA   = "openwa"
)

// Config is a channel's sealed configuration.
type Config struct {
	URL    string `json:"url"`               // shoutrrr URL, or OpenWA base URL
	APIKey string `json:"api_key,omitempty"` // OpenWA
	Chat   string `json:"chat,omitempty"`    // OpenWA contact or group ID
}

// Message is what a template renders.
type Message struct {
	Type    string
	Subject string
	Name    string // client or node display name
	Site    string
	Time    time.Time
	Payload map[string]any
}

var defaultTemplates = map[string]string{
	"client.joined":    `🆕 New device on {{.Site}}: {{.Name}}{{with .Payload.ip}} ({{.}}){{end}}`,
	"client.online":    `🟢 {{.Name}} is online on {{.Site}}`,
	"client.offline":   `⚪ {{.Name}} went offline on {{.Site}}`,
	"node.online":      `✅ Deco {{.Name}} is back online`,
	"node.offline":     `🔴 Deco {{.Name}} is offline`,
	"wan.up":           `🌐 Internet is back on {{.Site}}`,
	"wan.down":         `🚫 Internet is down on {{.Site}}{{with .Payload.error}} ({{.}}){{end}}`,
	"wan.ip_changed":   `🔁 WAN IP changed on {{.Site}}: {{.Payload.from}} → {{.Payload.to}}`,
	"firmware.changed": `⬆️ Deco {{.Name}} firmware {{.Payload.from}} → {{.Payload.to}}`,
	"test":             `✅ Atlantis test message for {{.Site}}`,
}

// Render renders tmpl (or the default for m.Type) for m.
func Render(tmpl string, m Message) (string, error) {
	if tmpl == "" {
		tmpl = defaultTemplates[m.Type]
	}
	if tmpl == "" {
		tmpl = `{{.Type}}: {{.Name}}`
	}
	t, err := template.New("msg").Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("template: %w", err)
	}
	var b strings.Builder
	if err := t.Execute(&b, m); err != nil {
		return "", fmt.Errorf("template: %w", err)
	}
	return strings.ReplaceAll(b.String(), "<no value>", ""), nil
}

// Sender sends one text message.
type Sender interface {
	Send(ctx context.Context, text string) error
}

type shoutrrrSender struct{ url string }

// Send honours ctx: Shoutrrr itself has no deadline (it uses
// http.DefaultClient), so the send runs aside and is abandoned when ctx
// ends. serve also bounds http.DefaultClient so abandoned sends finish.
func (s shoutrrrSender) Send(ctx context.Context, text string) error {
	done := make(chan error, 1)
	go func() { done <- shoutrrr.Send(s.url, text) }()
	select {
	case err := <-done:
		if err != nil {
			return redact(err, urlSecrets(s.url)...)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("shoutrrr: %w", ctx.Err())
	}
}

// openWASender posts to OpenWA's /sendText with the api_key header.
type openWASender struct {
	cfg  Config
	http *http.Client
}

func (s openWASender) Send(ctx context.Context, text string) error {
	body, _ := json.Marshal(map[string]any{"args": map[string]string{"to": s.cfg.Chat, "content": text}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.URL, "/")+"/sendText", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.APIKey != "" {
		req.Header.Set("api_key", s.cfg.APIKey)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return redact(err, append(urlSecrets(s.cfg.URL), s.cfg.APIKey, s.cfg.Chat)...)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("openwa: HTTP %d", resp.StatusCode)
	}
	return nil
}

// redact removes secrets from an error message. Services quote the URL
// they called, which differs from the configured one, so every secret part
// is replaced on its own.
func redact(err error, secrets ...string) error {
	msg := err.Error()
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, s := range secrets {
		if len(s) >= 4 {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	return errors.New(msg)
}

// urlSecrets lists the parts of a notification URL that may be secret: the
// whole URL, user info, every query value and every path segment (Telegram
// and Discord put tokens in the path).
func urlSecrets(raw string) []string {
	out := []string{raw}
	u, err := url.Parse(raw)
	if err != nil {
		return out
	}
	if u.User != nil {
		out = append(out, u.User.Username())
		if p, ok := u.User.Password(); ok {
			out = append(out, p)
		}
	}
	for _, vs := range u.Query() {
		out = append(out, vs...)
	}
	out = append(out, strings.Split(u.Path, "/")...)
	return out
}

// NewSender builds the sender for a channel kind and its opened config.
func NewSender(kind string, c Config) (Sender, error) {
	switch kind {
	case KindShoutrrr:
		if c.URL == "" {
			return nil, errors.New("shoutrrr: a URL is required")
		}
		return shoutrrrSender{url: c.URL}, nil
	case KindOpenWA:
		if c.URL == "" || c.Chat == "" {
			return nil, errors.New("openwa: the OpenWA address and the chat are required")
		}
		return openWASender{cfg: c, http: &http.Client{Timeout: 15 * time.Second}}, nil
	}
	return nil, fmt.Errorf("unknown channel kind %q", kind)
}

// Dispatcher routes events to the enabled channels.
type Dispatcher struct {
	store *store.Store
	box   *secret.Box

	mu    sync.RWMutex // guards the settings below (changed from the UI)
	quiet [2]int       // minutes after midnight; equal = no quiet hours
	loc   *time.Location

	site    func() string
	log     *slog.Logger
	queue   chan job
	now     func() time.Time
	newSend func(kind string, c Config) (Sender, error)
}

type job struct {
	msg     Message
	watched bool
}

// New returns a dispatcher (UTC, no quiet hours until SetPrefs); call Run
// to start delivering.
func New(st *store.Store, box *secret.Box, site func() string, log *slog.Logger) *Dispatcher {
	return &Dispatcher{store: st, box: box, site: site, log: log, loc: time.UTC,
		queue: make(chan job, 256), now: time.Now, newSend: NewSender}
}

// SetPrefs applies quiet hours ("HH:MM-HH:MM", "" for none) and the time
// zone; the next notification uses them. Values are validated by the caller.
func (d *Dispatcher) SetPrefs(quietHours, timezone string) {
	loc, _ := time.LoadLocation(timezone)
	if loc == nil {
		loc = time.UTC
	}
	var quiet [2]int
	if quietHours != "" {
		s, e, _ := config.ParseQuietHours(quietHours)
		quiet = [2]int{s, e}
	}
	d.mu.Lock()
	d.loc, d.quiet = loc, quiet
	d.mu.Unlock()
}

// Run delivers queued events until ctx is done.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-d.queue:
			d.deliver(ctx, j)
		}
	}
}

// Notify queues an event. It never blocks: when the queue is full the
// event is dropped and logged. watched says whether the subject is a
// watched client (for watched-only channels).
func (d *Dispatcher) Notify(e store.Event, name string, watched bool) {
	var p map[string]any
	_ = json.Unmarshal(e.Payload, &p)
	select {
	case d.queue <- job{Message{Type: e.Type, Subject: e.Subject, Name: name, Site: d.site(), Time: e.TS, Payload: p}, watched}:
	default:
		d.log.Warn("notification queue full; event dropped", "type", e.Type)
	}
}

func (d *Dispatcher) inQuietHours(t time.Time) bool {
	d.mu.RLock()
	s, e, loc := d.quiet[0], d.quiet[1], d.loc
	d.mu.RUnlock()
	if s == e {
		return false
	}
	lt := t.In(loc)
	m := lt.Hour()*60 + lt.Minute()
	if s < e {
		return m >= s && m < e
	}
	return m >= s || m < e // wraps past midnight
}

func (d *Dispatcher) deliver(ctx context.Context, j job) {
	if d.inQuietHours(d.now()) {
		return
	}
	chans, err := d.store.Channels(ctx)
	if err != nil {
		d.log.Error("listing channels", "err", err)
		return
	}
	for _, ch := range chans {
		if !ch.Enabled || (len(ch.Events) > 0 && !slices.Contains(ch.Events, j.msg.Type)) {
			continue
		}
		if ch.WatchedOnly && strings.HasPrefix(j.msg.Type, "client.") && !j.watched {
			continue
		}
		if err := d.sendTo(ctx, ch, j.msg); err != nil {
			d.log.Warn("notification failed", "channel", ch.Name, "type", j.msg.Type, "err", err)
		}
	}
}

// OpenConfig decrypts a channel's config.
func (d *Dispatcher) OpenConfig(ch store.Channel) (Config, error) {
	raw, err := d.box.Open(ch.ConfigEnc)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Config{}, fmt.Errorf("channel config: %w", err)
	}
	return c, nil
}

// FillOpenWA copies def into the empty fields of every OpenWA channel. It
// moves the OpenWA defaults earlier versions had into the channels that
// relied on them. A channel that can't be read or saved is logged and
// skipped. It returns how many channels changed.
func (d *Dispatcher) FillOpenWA(ctx context.Context, def Config) (int, error) {
	chs, err := d.store.Channels(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ch := range chs {
		if ch.Kind != KindOpenWA {
			continue
		}
		c, err := d.OpenConfig(ch)
		if err != nil {
			d.log.Warn("skipping a WhatsApp channel whose settings can't be read", "channel", ch.Name, "err", err)
			continue
		}
		was := c
		if c.URL == "" {
			c.URL = def.URL
		}
		if c.APIKey == "" {
			c.APIKey = def.APIKey
		}
		if c.Chat == "" {
			c.Chat = def.Chat
		}
		if c == was {
			continue
		}
		if ch.ConfigEnc, err = d.SealConfig(c); err != nil {
			return n, err
		}
		if _, err := d.store.SaveChannel(ctx, ch); err != nil {
			d.log.Warn("could not save a WhatsApp channel", "channel", ch.Name, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

// SealConfig encrypts a channel config for storage.
func (d *Dispatcher) SealConfig(c Config) (string, error) {
	b, _ := json.Marshal(c)
	return d.box.Seal(string(b))
}

func (d *Dispatcher) sendTo(ctx context.Context, ch store.Channel, m Message) error {
	c, err := d.OpenConfig(ch)
	if err != nil {
		return err
	}
	s, err := d.newSend(ch.Kind, c)
	if err != nil {
		return err
	}
	text, err := Render(ch.Template, m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return s.Send(ctx, text)
}

// Test sends a test message to a channel synchronously, ignoring quiet
// hours and filters, and returns the delivery error.
func (d *Dispatcher) Test(ctx context.Context, ch store.Channel) error {
	return d.sendTo(ctx, ch, Message{Type: "test", Site: d.site(), Time: d.now()})
}
