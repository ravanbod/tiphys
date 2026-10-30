package application

import (
	"context"
	"sync"
	"time"
)

// OpEvent is one progress or completion event of an Operation.
type OpEvent struct {
	Name string // "progress" or "done"
	Data any
}

// OpDone is the payload of the final "done" event.
type OpDone struct {
	Outcome string `json:"outcome"` // succeeded, failed, timed_out
	Message string `json:"message"`
}

// RolloutProgress is the payload of a restart's progress events.
type RolloutProgress struct {
	Desired   int32 `json:"desired"`
	Updated   int32 `json:"updated"`
	Ready     int32 `json:"ready"`
	Available int32 `json:"available"`
}

// Operation is a running restart or job run whose progress browsers follow
// over Server-Sent Events. Late subscribers get the latest progress and the
// final event replayed.
type Operation struct {
	ID      string
	OwnerID int64
	created time.Time

	mu       sync.Mutex
	progress *OpEvent
	final    *OpEvent
	subs     map[int]chan OpEvent
	next     int
}

// Publish delivers an event to subscribers and remembers it for replay.
func (o *Operation) Publish(ev OpEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.final != nil {
		return
	}
	if ev.Name == "done" {
		o.final = &ev
	} else {
		o.progress = &ev
	}
	for _, ch := range o.subs {
		select {
		case ch <- ev:
		default:
			// A slow subscriber misses an intermediate progress event; it
			// still receives the final one because Subscribe replays it.
		}
	}
}

// Subscribe returns the events to replay, a channel for new ones and a
// cancel function.
func (o *Operation) Subscribe() ([]OpEvent, <-chan OpEvent, func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var replay []OpEvent
	if o.progress != nil {
		replay = append(replay, *o.progress)
	}
	if o.final != nil {
		replay = append(replay, *o.final)
		ch := make(chan OpEvent)
		close(ch)
		return replay, ch, func() {}
	}
	id := o.next
	o.next++
	ch := make(chan OpEvent, 32)
	o.subs[id] = ch
	return replay, ch, func() {
		o.mu.Lock()
		delete(o.subs, id)
		o.mu.Unlock()
	}
}

// Operations is the in-memory registry of running operations.
type Operations struct {
	mu  sync.Mutex
	ops map[string]*Operation
}

func NewOperations() *Operations { return &Operations{ops: map[string]*Operation{}} }

// Create registers a new operation owned by a user.
func (r *Operations) Create(ownerID int64, now time.Time) *Operation {
	o := &Operation{ID: randomToken(12), OwnerID: ownerID, created: now, subs: map[int]chan OpEvent{}}
	r.mu.Lock()
	r.ops[o.ID] = o
	r.mu.Unlock()
	return o
}

// Get returns an operation by id.
func (r *Operations) Get(id string) (*Operation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.ops[id]
	return o, ok
}

// Run removes old operations until ctx is done.
func (r *Operations) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			r.mu.Lock()
			for id, o := range r.ops {
				if now.Sub(o.created) > time.Hour {
					delete(r.ops, id)
				}
			}
			r.mu.Unlock()
		}
	}
}
