package api

import (
	"context"
	"strconv"
	"sync"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
)

const defaultReplayBuffer = 1024

type Hub struct {
	mu          sync.RWMutex
	next        uint64
	ring        []*governv1.StreamEvent
	ringSize    int
	subscribers map[int]subscriber
	nextSubID   int
}

type subscriber struct {
	stream chan *governv1.StreamEvent
	allow  Filter
}

func NewHub(replayBuffer int) *Hub {
	if replayBuffer <= 0 {
		replayBuffer = defaultReplayBuffer
	}

	return &Hub{
		ringSize:    replayBuffer,
		subscribers: map[int]subscriber{},
	}
}

func (h *Hub) Publish(event *governv1.StreamEvent) {
	h.mu.Lock()

	h.next++
	event.Cursor = strconv.FormatUint(h.next, 10)

	h.ring = append(h.ring, event)
	if len(h.ring) > h.ringSize {
		h.ring = h.ring[len(h.ring)-h.ringSize:]
	}

	targets := make([]subscriber, 0, len(h.subscribers))
	for _, target := range h.subscribers {
		targets = append(targets, target)
	}

	h.mu.Unlock()

	for _, target := range targets {
		if !target.allow(event) {
			continue
		}

		select {
		case target.stream <- event:
		default:
		}
	}
}

type Filter func(event *governv1.StreamEvent) bool

func (h *Hub) Subscribe(ctx context.Context, from string, allow Filter) (<-chan *governv1.StreamEvent, bool) {
	if allow == nil {
		allow = func(*governv1.StreamEvent) bool { return true }
	}

	backlog, complete := h.replay(from)

	stream := make(chan *governv1.StreamEvent, len(backlog)+64)
	for _, event := range backlog {
		if allow(event) {
			stream <- event
		}
	}

	h.mu.Lock()
	id := h.nextSubID
	h.nextSubID++
	h.subscribers[id] = subscriber{stream: stream, allow: allow}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		delete(h.subscribers, id)
		h.mu.Unlock()
		close(stream)
	}()

	return stream, complete
}

func (h *Hub) replay(from string) (events []*governv1.StreamEvent, complete bool) {
	if from == "" {
		return nil, true
	}

	after, err := strconv.ParseUint(from, 10, 64)
	if err != nil {
		return nil, false
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.ring) == 0 {
		return nil, after == h.next
	}

	oldest, err := strconv.ParseUint(h.ring[0].GetCursor(), 10, 64)
	if err != nil {
		return nil, false
	}
	if after+1 < oldest {
		return nil, false
	}

	for _, event := range h.ring {
		cursor, err := strconv.ParseUint(event.GetCursor(), 10, 64)
		if err != nil {
			continue
		}
		if cursor > after {
			events = append(events, event)
		}
	}

	return events, true
}

func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}
