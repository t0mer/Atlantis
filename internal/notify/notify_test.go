package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/secret"
	"github.com/t0mer/atlantis/internal/store"
)

type recorder struct {
	mu   sync.Mutex
	sent []string
}

func (r *recorder) Send(_ context.Context, text string) error {
	r.mu.Lock()
	r.sent = append(r.sent, text)
	r.mu.Unlock()
	return nil
}

func setup(t *testing.T, quiet string) (*Dispatcher, *store.Store, *recorder, *time.Time) {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secret.New(secret.NewKey())
	cfg := &config.Config{Notify: config.Notify{Timezone: "Asia/Jerusalem", QuietHours: quiet}}
	d := New(st, box, cfg, func() string { return "Home" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := &recorder{}
	d.newSend = func(string, Config) (Sender, error) { return rec, nil }
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC) // 12:00 in Jerusalem
	d.now = func() time.Time { return now }
	return d, st, rec, &now
}

func addChannel(t *testing.T, d *Dispatcher, st *store.Store, ch store.Channel) {
	t.Helper()
	enc, err := d.SealConfig(Config{URL: "telegram://token@telegram?chats=1"})
	if err != nil {
		t.Fatal(err)
	}
	ch.ConfigEnc, ch.Kind = enc, KindShoutrrr
	if _, err := st.SaveChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
}

func ev(typ string) store.Event {
	return store.Event{Type: typ, Subject: "aa:bb", Payload: json.RawMessage(`{"ip":"192.0.2.5","from":"1.0","to":"1.1"}`)}
}

func TestFiltersAndTemplates(t *testing.T) {
	d, st, rec, _ := setup(t, "")
	addChannel(t, d, st, store.Channel{Name: "all", Enabled: true})
	addChannel(t, d, st, store.Channel{Name: "nodes only", Enabled: true, Events: []string{"node.offline"}})
	addChannel(t, d, st, store.Channel{Name: "watched", Enabled: true, WatchedOnly: true, Template: "W {{.Name}}"})
	addChannel(t, d, st, store.Channel{Name: "off", Enabled: false})
	ctx := context.Background()
	d.deliver(ctx, job{msg: Message{Type: "client.joined", Name: "Phone", Site: "Home", Payload: map[string]any{"ip": "192.0.2.5"}}, watched: false})
	d.deliver(ctx, job{msg: Message{Type: "node.offline", Name: "Office", Site: "Home"}})
	d.deliver(ctx, job{msg: Message{Type: "client.offline", Name: "Kid", Site: "Home"}, watched: true})
	got := strings.Join(rec.sent, "|")
	want := "🆕 New device on Home: Phone (192.0.2.5)|🔴 Deco Office is offline|🔴 Deco Office is offline|W Office|⚪ Kid went offline on Home|W Kid"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestQuietHoursWrapMidnight(t *testing.T) {
	d, st, rec, now := setup(t, "23:00-07:00")
	addChannel(t, d, st, store.Channel{Name: "all", Enabled: true})
	*now = time.Date(2026, 11, 25, 22, 30, 0, 0, time.UTC) // 00:30 in Jerusalem
	d.deliver(context.Background(), job{msg: Message{Type: "node.offline"}})
	if len(rec.sent) != 0 {
		t.Fatal("sent during quiet hours")
	}
	*now = time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC) // 08:00
	d.deliver(context.Background(), job{msg: Message{Type: "node.offline"}})
	if len(rec.sent) != 1 {
		t.Fatal("not sent outside quiet hours")
	}
}

func TestNotifyNeverBlocks(t *testing.T) {
	d, _, _, _ := setup(t, "")
	done := make(chan struct{})
	go func() {
		for range 1000 { // far more than the queue holds; Run is not started
			d.Notify(ev("client.joined"), "x", false)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify blocked")
	}
}

func TestOpenWASenderAndRedaction(t *testing.T) {
	var got map[string]any
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("api_key")
		json.NewDecoder(r.Body).Decode(&got)
		if r.URL.Path != "/sendText" {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s, err := NewSender(KindOpenWA, Config{URL: srv.URL, APIKey: "k3y", Chat: "123@g.us"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	args := got["args"].(map[string]any)
	if key != "k3y" || args["to"] != "123@g.us" || args["content"] != "hi" {
		t.Fatalf("%v %s", got, key)
	}
	srv.Close()
	err = s.Send(context.Background(), "hi")
	if err == nil || strings.Contains(err.Error(), "k3y") {
		t.Fatalf("error missing or leaks the key: %v", err)
	}
	if _, err := NewSender(KindShoutrrr, Config{}); err == nil {
		t.Fatal("empty URL accepted")
	}
}

func TestSecretsAreSealed(t *testing.T) {
	d, st, _, _ := setup(t, "")
	addChannel(t, d, st, store.Channel{Name: "tg", Enabled: true})
	chs, _ := st.Channels(context.Background())
	if strings.Contains(chs[0].ConfigEnc, "token") {
		t.Fatal("channel URL stored in plaintext")
	}
	c, err := d.OpenConfig(chs[0])
	if err != nil || !strings.HasPrefix(c.URL, "telegram://") {
		t.Fatal(c, err)
	}
}
