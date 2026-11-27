// Package events is the in-process pub/sub feeding the SSE /stream endpoint.
package events

import "sync"

// Msg is one SSE event.
type Msg struct {
	Type    string // box.updated | event.created | approval.created | overview.updated
	Data    any
	AgentID string // empty = visible to admins only (and all for overview)
	Public  bool   // visible to every subscriber
	// WorkspaceID (v2.0): only callers of this workspace (and global
	// platform callers) receive the message; "" = platform-wide.
	WorkspaceID string
}

type Broker struct {
	mu   sync.RWMutex
	subs map[chan Msg]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan Msg]struct{}{}} }

func (b *Broker) Subscribe() chan Msg {
	ch := make(chan Msg, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) Unsubscribe(ch chan Msg) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
	b.mu.Unlock()
}

// Publish never blocks: slow subscribers drop messages.
func (b *Broker) Publish(m Msg) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

func (b *Broker) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
