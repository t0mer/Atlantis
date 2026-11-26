package site

import (
	"context"
	"fmt"
	"time"

	"github.com/t0mer/atlantis/internal/config"
	"github.com/t0mer/atlantis/internal/poller"
)

// Poll are the refresh intervals. Values saved from the UI (settings key
// "poll") replace DefaultPoll.
type Poll struct {
	Fast               time.Duration `json:"fast"`
	Slow               time.Duration `json:"slow"`
	OfflineAfterMisses int           `json:"offline_after_misses"`
}

// DefaultPoll applies until refresh intervals are saved.
var DefaultPoll = Poll{Fast: 30 * time.Second, Slow: 5 * time.Minute, OfflineAfterMisses: 2}

const pollKey = "poll"

// MaxPoll is the longest refresh interval accepted.
const MaxPoll = time.Hour

// Validate checks the intervals the UI may set.
func (p Poll) Validate() error {
	switch {
	case p.Fast < config.MinPoll || p.Slow < config.MinPoll:
		return fmt.Errorf("%w: refresh intervals must be at least %s", ErrInvalid, config.MinPoll)
	case p.Fast > MaxPoll || p.Slow > MaxPoll:
		return fmt.Errorf("%w: refresh intervals must be at most %s", ErrInvalid, MaxPoll)
	case p.Slow < p.Fast:
		return fmt.Errorf("%w: the mesh check cannot be more frequent than the device check", ErrInvalid)
	case p.OfflineAfterMisses < 1 || p.OfflineAfterMisses > 10:
		return fmt.Errorf("%w: offline after must be 1 to 10 missed checks", ErrInvalid)
	}
	return nil
}

// Poll returns the intervals in effect.
func (s *Site) Poll() Poll {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.poll
}

// LoadPoll reads intervals saved from the UI, if any.
func (s *Site) LoadPoll(ctx context.Context) error {
	var p Poll
	ok, err := s.d.Store.GetSetting(ctx, pollKey, &p)
	if err != nil || !ok {
		return err
	}
	if err := p.Validate(); err != nil {
		s.d.Log.Warn("ignoring saved refresh intervals", "err", err)
		return nil
	}
	s.mu.Lock()
	s.poll = p
	s.mu.Unlock()
	return nil
}

// SetPoll validates, saves and applies new intervals; the running poller
// switches to them at once.
func (s *Site) SetPoll(ctx context.Context, p Poll) error {
	if err := p.Validate(); err != nil {
		return err
	}
	s.pollMu.Lock() // what is stored and what runs stay the same
	defer s.pollMu.Unlock()
	if err := s.d.Store.SetSetting(ctx, pollKey, p); err != nil {
		return err
	}
	s.mu.Lock()
	s.poll = p
	run := s.run
	s.mu.Unlock()
	if run != nil {
		run.Poller.SetIntervals(poller.Intervals{Fast: p.Fast, Slow: p.Slow, OfflineAfter: p.OfflineAfterMisses})
	}
	return nil
}
