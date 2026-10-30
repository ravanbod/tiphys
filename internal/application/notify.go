package application

import (
	"context"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/domain"
)

// notificationRetryDelays gives up to 5 retries over about 10 minutes.
var notificationRetryDelays = []time.Duration{10 * time.Second, 30 * time.Second, 90 * time.Second, 3 * time.Minute, 5 * time.Minute}

type notificationJob struct {
	msg     NotificationMessage
	attempt int
}

// NotificationService sends Rocket.Chat messages asynchronously. A failure
// is audited and never blocks the action that caused it.
type NotificationService struct {
	notifier  Notifier
	settings  *SettingsService
	audit     *AuditService
	publicURL string
	logger    *zap.Logger
	queue     chan notificationJob
}

func NewNotificationService(notifier Notifier, settings *SettingsService, audit *AuditService, publicURL string, logger *zap.Logger) *NotificationService {
	return &NotificationService{
		notifier: notifier, settings: settings, audit: audit, publicURL: publicURL, logger: logger,
		queue: make(chan notificationJob, 1000),
	}
}

// Link builds an absolute link to a portal page.
func (n *NotificationService) Link(path string) string { return n.publicURL + path }

// Notify queues a message if the event's toggle and the webhook are set.
func (n *NotificationService) Notify(msg NotificationMessage) {
	s := n.settings.Get().Rocket
	if s.WebhookURL == "" || !s.Events[msg.Event] {
		return
	}
	select {
	case n.queue <- notificationJob{msg: msg}:
	default:
		n.logger.Error("notification queue full, message dropped", zap.String("event", string(msg.Event)))
	}
}

// SendTest sends a test message synchronously and returns the error.
func (n *NotificationService) SendTest(ctx context.Context, by string) error {
	s := n.settings.Get().Rocket
	if s.WebhookURL == "" {
		return Invalid("webhookUrl", "webhook URL is not set")
	}
	return n.notifier.Send(ctx, s.WebhookURL, s.Channel, NotificationMessage{
		Title:  "Tiphys test notification",
		Fields: []NotificationField{{Name: "Sent by", Value: by}},
		Link:   n.Link("/"),
	})
}

// Run delivers queued messages until ctx is done.
func (n *NotificationService) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-n.queue:
			n.deliver(ctx, job)
		}
	}
}

func (n *NotificationService) deliver(ctx context.Context, job notificationJob) {
	s := n.settings.Get().Rocket
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := n.notifier.Send(sendCtx, s.WebhookURL, s.Channel, job.msg)
	cancel()
	if err == nil {
		return
	}
	if job.attempt >= len(notificationRetryDelays) {
		n.logger.Error("notification failed permanently", zap.String("event", string(job.msg.Event)), zap.Error(err))
		e := n.audit.SystemEvent(domain.EventNotificationFail)
		e.Result = domain.ResultFailed
		e.Reason = err.Error()
		e.Details = map[string]any{"event": string(job.msg.Event), "title": job.msg.Title}
		n.audit.RecordBestEffort(ctx, e)
		return
	}
	delay := notificationRetryDelays[job.attempt]
	job.attempt++
	n.logger.Warn("notification failed, will retry", zap.String("event", string(job.msg.Event)), zap.Duration("retry_in", delay), zap.Error(err))
	time.AfterFunc(delay, func() {
		select {
		case n.queue <- job:
		default:
		}
	})
}
