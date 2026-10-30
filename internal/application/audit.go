package application

import (
	"context"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
)

const auditQueueSize = 10000

// AuditService writes audit events to PostgreSQL (for the UI) and pushes
// them to the external sink (the copy the portal cannot alter).
//
// The PostgreSQL write is synchronous and its failure is returned, so a
// caller can refuse to run an action whose "started" event was not stored.
// The sink push is queued, retried in the background and never blocks.
type AuditService struct {
	store  Store
	sink   AuditSink
	clock  Clock
	logger *zap.Logger
	queue  chan domain.AuditEvent
}

func NewAuditService(store Store, sink AuditSink, clock Clock, logger *zap.Logger) *AuditService {
	return &AuditService{
		store:  store,
		sink:   sink,
		clock:  clock,
		logger: logger,
		queue:  make(chan domain.AuditEvent, auditQueueSize),
	}
}

// Event starts an audit event for the principal.
func (a *AuditService) Event(p Principal, eventType string) domain.AuditEvent {
	uid := p.User.ID
	e := domain.AuditEvent{
		EventType: eventType,
		Username:  p.User.Username,
		SourceIP:  p.SourceIP,
		UserAgent: p.UserAgent,
	}
	if uid != 0 {
		e.UserID = &uid
	}
	return e
}

// SystemEvent starts an audit event not made by a user.
func (a *AuditService) SystemEvent(eventType string) domain.AuditEvent {
	return domain.AuditEvent{EventType: eventType, Username: "system"}
}

// Record stores the event in PostgreSQL and queues it for the sink.
func (a *AuditService) Record(ctx context.Context, e domain.AuditEvent) (domain.AuditEvent, error) {
	stored, err := a.RecordIn(ctx, a.store.Repos(), e)
	if err != nil {
		return e, err
	}
	a.Publish(stored)
	return stored, nil
}

// RecordIn stores the event with the given repositories (inside a
// transaction). The caller calls Publish after the transaction commits.
func (a *AuditService) RecordIn(ctx context.Context, repos Repositories, e domain.AuditEvent) (domain.AuditEvent, error) {
	if e.At.IsZero() {
		e.At = a.clock.Now()
	}
	stored, err := repos.Audit.Insert(ctx, e)
	if err != nil {
		return e, err
	}
	return stored, nil
}

// Publish queues a stored event for the external sink. It never blocks: if
// the queue is full the oldest event is dropped (it is still in PostgreSQL).
func (a *AuditService) Publish(e domain.AuditEvent) {
	if a.sink == nil {
		return
	}
	for {
		select {
		case a.queue <- e:
			return
		default:
		}
		select {
		case <-a.queue:
			monitoring.AuditQueueDropped.Inc()
		default:
		}
	}
}

// RecordBestEffort records an event whose failure must not block the caller
// (denials, notification failures); it logs the failure.
func (a *AuditService) RecordBestEffort(ctx context.Context, e domain.AuditEvent) {
	if _, err := a.Record(ctx, e); err != nil {
		a.logger.Error("failed to write audit event", zap.String("event_type", e.EventType), zap.Error(err))
	}
}

// Query lists audit events.
func (a *AuditService) Query(ctx context.Context, f AuditFilter) ([]domain.AuditEvent, error) {
	return a.store.Repos().Audit.List(ctx, f)
}

// Run pushes queued events to the sink until ctx is done, retrying failures
// with backoff. Call it in its own goroutine.
func (a *AuditService) Run(ctx context.Context) {
	if a.sink == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			a.drain()
			return
		case e := <-a.queue:
			a.push(ctx, e)
		}
	}
}

func (a *AuditService) push(ctx context.Context, e domain.AuditEvent) {
	backoff := time.Second
	for {
		pushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := a.sink.Push(pushCtx, e)
		cancel()
		if err == nil {
			return
		}
		monitoring.AuditSinkErrors.Inc()
		a.logger.Error("failed to push audit event to sink", zap.Int64("event_id", e.ID), zap.Duration("retry_in", backoff), zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// drain makes one last attempt for the events still queued at shutdown.
func (a *AuditService) drain() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		select {
		case e := <-a.queue:
			if err := a.sink.Push(ctx, e); err != nil {
				a.logger.Error("failed to push audit event to sink at shutdown", zap.Int64("event_id", e.ID), zap.Error(err))
				return
			}
		default:
			return
		}
	}
}
