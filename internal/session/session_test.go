package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/atlantis/internal/deco"
)

// fakeClient returns err on every call and counts logouts.
type fakeClient struct {
	deco.Client
	mu      sync.Mutex
	err     error
	calls   int
	logouts int
}

func (f *fakeClient) Devices(context.Context) ([]deco.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return nil, f.err
}
func (f *fakeClient) Logout(context.Context) error {
	f.mu.Lock()
	f.logouts++
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) Raw(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}

func call(m *Manager) error {
	return m.Do(context.Background(), func(ctx context.Context, c deco.Client) error {
		_, err := c.Devices(ctx)
		return err
	})
}

func setup(t *testing.T) (*Manager, *fakeClient, *time.Time, *[]State) {
	f := &fakeClient{}
	var states []State
	m := New(f, func(s Status) { states = append(states, s.State) })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.SetClock(func() time.Time { return now })
	return m, f, &now, &states
}

func TestAuthFailureStopsWithoutRetrying(t *testing.T) {
	m, f, _, _ := setup(t)
	f.err = fmt.Errorf("ssh: %w", deco.ErrWrongPassword)
	call(m)
	if m.Status().State != StateAuthFailed || f.logouts != 1 {
		t.Fatalf("%+v", m.Status())
	}
	for range 5 {
		if err := call(m); !errors.Is(err, ErrStopped) {
			t.Fatal(err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("the device was contacted %d times after a rejected password", f.calls)
	}
	// Review finding: no path may send the rejected password again.
	f.err = nil
	m.Resume()
	m.Release(context.Background(), time.Minute)
	m.Pause(context.Background())
	m.Resume()
	m.ClearCert()
	for range 3 {
		if err := call(m); !errors.Is(err, ErrStopped) {
			t.Fatalf("resumed after auth_failed: %v", err)
		}
	}
	if f.calls != 1 || m.Status().State != StateAuthFailed {
		t.Fatalf("calls=%d state=%s", f.calls, m.Status().State)
	}
}

func TestReleaseExpiryAfterAuthFailedStaysStopped(t *testing.T) {
	m, f, now, _ := setup(t)
	f.err = deco.ErrWrongPassword
	call(m)
	m.Release(context.Background(), time.Minute)
	*now = now.Add(2 * time.Minute)
	f.err = nil
	if !errors.Is(call(m), ErrStopped) || f.calls != 1 {
		t.Fatal("release expiry retried the password")
	}
}

func TestClearCertLiftsOnlyCertStops(t *testing.T) {
	m, f, _, _ := setup(t)
	f.err = deco.ErrCertChanged
	call(m)
	f.err = nil
	m.ClearCert()
	if call(m) != nil || m.Status().State != StateConnected {
		t.Fatal("accepting the key must resume")
	}
}

func TestOtherAdminBacksOffExponentially(t *testing.T) {
	m, f, now, _ := setup(t)
	f.err = deco.ErrOtherAdmin
	call(m)
	st := m.Status()
	if st.State != StateBlocked || st.RetryAt.Sub(*now) != 30*time.Second {
		t.Fatalf("%+v", st)
	}
	if err := call(m); !errors.Is(err, ErrBlocked) || f.calls != 1 {
		t.Fatal("called during backoff")
	}
	for _, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute} {
		*now = m.Status().RetryAt
		call(m)
		if got := m.Status().RetryAt.Sub(*now); got != want {
			t.Fatalf("backoff %s, want %s", got, want)
		}
	}
	*now = m.Status().RetryAt
	f.err = nil
	if call(m) != nil || m.Status().State != StateConnected {
		t.Fatal("recovery")
	}
	f.err = deco.ErrOtherAdmin
	call(m)
	if m.Status().RetryAt.Sub(*now) != 30*time.Second {
		t.Fatal("backoff must reset after a success")
	}
}

func TestCertStatesStop(t *testing.T) {
	for err, want := range map[error]State{deco.ErrCertChanged: StateCertChanged, deco.ErrCertUnknown: StateCertUnknown} {
		m, f, _, _ := setup(t)
		f.err = err
		call(m)
		if m.Status().State != want || !errors.Is(call(m), ErrStopped) {
			t.Fatalf("%v: %+v", err, m.Status())
		}
	}
}

func TestTransientAndUnsupported(t *testing.T) {
	m, f, _, _ := setup(t)
	f.err = errors.New("connection reset")
	call(m)
	if m.Status().State != StateDisconnected {
		t.Fatal(m.Status())
	}
	f.err = nil
	call(m)
	f.err = fmt.Errorf("wan: %w", deco.ErrUnsupported)
	call(m)
	if m.Status().State != StateConnected {
		t.Fatal("unsupported must not change the session state")
	}
}

func TestPauseAndRelease(t *testing.T) {
	m, f, now, states := setup(t)
	call(m)
	m.Pause(context.Background())
	if !errors.Is(call(m), ErrPaused) || f.logouts != 1 {
		t.Fatal("pause")
	}
	m.Resume()
	if call(m) != nil {
		t.Fatal("resume")
	}
	m.Release(context.Background(), 10*time.Minute)
	if !errors.Is(call(m), ErrPaused) {
		t.Fatal("release")
	}
	*now = now.Add(11 * time.Minute)
	if call(m) != nil || m.Status().State != StateConnected {
		t.Fatal("release must end by itself")
	}
	if len(*states) < 4 {
		t.Fatalf("state changes not reported: %v", *states)
	}
}

func TestCallsAreSerialised(t *testing.T) {
	m, _, _, _ := setup(t)
	var active, peak int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Do(context.Background(), func(context.Context, deco.Client) error {
				mu.Lock()
				active++
				peak = max(peak, active)
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				mu.Lock()
				active--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Fatalf("%d calls ran at once", peak)
	}
}

// A call that was waiting when the connection was closed must not sign in
// again: nobody would ever log that session out.
func TestNoCallsAfterClose(t *testing.T) {
	f := &fakeClient{}
	m := New(f, nil)
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ran := false
	err := m.Do(context.Background(), func(context.Context, deco.Client) error { ran = true; return nil })
	if !errors.Is(err, ErrClosed) || ran {
		t.Fatalf("Do after Close: %v ran=%v", err, ran)
	}
	if err := m.Pause(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Pause after Close: %v", err)
	}
}

// A connection error wrapped in a request error still reaches the manager.
func TestWrappedConnectionErrorsStillCount(t *testing.T) {
	m := New(&fakeClient{}, nil)
	err := fmt.Errorf("%w: reading it back failed: %w", deco.ErrNotApplied, deco.ErrOtherAdmin)
	_ = m.Do(context.Background(), func(context.Context, deco.Client) error { return err })
	if st := m.Status().State; st != StateBlocked {
		t.Fatalf("state %s, want blocked", st)
	}
	m2 := New(&fakeClient{}, nil)
	_ = m2.Do(context.Background(), func(context.Context, deco.Client) error { return fmt.Errorf("%w: offline", deco.ErrNotFound) })
	if st := m2.Status().State; st == StateDisconnected {
		t.Fatal("a request error must not mark the session disconnected")
	}
}
