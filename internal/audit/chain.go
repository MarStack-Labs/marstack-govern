package audit

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var ErrChainBroken = errors.New("the audit chain does not verify")

type Sink interface {
	Tip(ctx context.Context) ([]byte, int64, error)
	Append(ctx context.Context, events []Event) error
}

type Archive interface {
	Write(ctx context.Context, events []Event) error
}

type Chain struct {
	sink    Sink
	archive Archive

	mu   sync.Mutex
	tip  []byte
	read bool
}

func NewChain(sink Sink, archive Archive) *Chain {
	return &Chain{sink: sink, archive: archive}
}

func (c *Chain) Append(ctx context.Context, events []Event) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.read {
		tip, _, err := c.sink.Tip(ctx)
		if err != nil {
			return 0, err
		}
		c.tip, c.read = tip, true
	}

	previous := c.tip
	linked := make([]Event, 0, len(events))

	for _, event := range events {
		canonical, err := event.Canonical()
		if err != nil {
			return 0, err
		}

		digest := Digest(previous, canonical)

		event.PrevHash = previous
		event.Hash = digest
		event.CanonicalBytes = canonical
		linked = append(linked, event)

		previous = digest
	}

	if err := c.sink.Append(ctx, linked); err != nil {
		c.read = false
		return 0, err
	}

	if c.archive != nil {
		if err := c.archive.Write(ctx, linked); err != nil {
			c.read = false
			return 0, fmt.Errorf("archive audit events: %w", err)
		}
	}

	c.tip = previous

	return len(linked), nil
}

type Verification struct {
	Intact         bool
	Checked        int64
	FirstSeq       int64
	LastSeq        int64
	BrokenAtSeq    int64
	Detail         string
	ArchiveChecked bool
}

func Verify(events []Event, expectedStart []byte) Verification {
	verification := Verification{Intact: true}

	previous := expectedStart

	for i, event := range events {
		verification.Checked++

		if string(event.PrevHash) != string(previous) {
			return broken(verification, int64(i),
				fmt.Sprintf("event %s does not follow the one before it", event.AuditID))
		}

		if string(Digest(previous, event.CanonicalBytes)) != string(event.Hash) {
			return broken(verification, int64(i),
				fmt.Sprintf("event %s was altered after it was recorded", event.AuditID))
		}

		if !event.Matches(event.CanonicalBytes) {
			return broken(verification, int64(i),
				fmt.Sprintf("the stored copy of event %s disagrees with what was signed", event.AuditID))
		}

		previous = event.Hash
		verification.LastSeq = int64(i)
	}

	return verification
}

func broken(verification Verification, at int64, detail string) Verification {
	verification.Intact = false
	verification.BrokenAtSeq = at
	verification.Detail = detail

	return verification
}
