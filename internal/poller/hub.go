package poller

import "sync"

// Message is one live update for subscribers (the SSE stream).
type Message struct {
	Kind string `json:"kind"` // snapshot, event, session
	Data any    `json:"data"`
}

// Hub fans messages out to subscribers. A slow subscriber loses messages
// rather than blocking the poller.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Message]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[chan Message]struct{}{}} }

// Subscribe returns a channel of messages and a cancel func.
func (h *Hub) Subscribe() (<-chan Message, func()) {
	ch := make(chan Message, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Publish sends m to every subscriber without blocking.
func (h *Hub) Publish(m Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}
