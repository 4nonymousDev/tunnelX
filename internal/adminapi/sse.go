package adminapi

import (
	"encoding/json"
	"sync"
)

type Event struct {
	Type string
	Data any
}

type eventHub struct {
	mu     sync.Mutex
	closed bool
	next   uint64
	subs   map[uint64]chan Event
}

func newEventHub() *eventHub { return &eventHub{subs: make(map[uint64]chan Event)} }

func (h *eventHub) publish(e Event) bool {
	if e.Type == "" {
		return false
	}
	if _, err := json.Marshal(e.Data); err != nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	for id, ch := range h.subs {
		select {
		case ch <- e:
		default:
			close(ch)
			delete(h.subs, id)
		}
	}
	return true
}

func (h *eventHub) subscribe() (<-chan Event, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, func() {}, false
	}
	h.next++
	id := h.next
	ch := make(chan Event, 16)
	h.subs[id] = ch
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if c, ok := h.subs[id]; ok {
				delete(h.subs, id)
				close(c)
			}
		})
	}, true
}

func (h *eventHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, ch := range h.subs {
		close(ch)
		delete(h.subs, id)
	}
}
