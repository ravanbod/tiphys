// Package rocketchat implements application.Notifier over a Rocket.Chat
// incoming webhook.
package rocketchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"go.uber.org/zap"

	"tiphys/internal/application"
)

const logBodyLimit = 1024

// HTTPNotifier posts messages to the webhook URL given on each call (it is an
// admin setting that can change at runtime).
type HTTPNotifier struct {
	httpClient *http.Client
	logger     *zap.Logger
}

func NewHTTPNotifier(httpClient *http.Client, logger *zap.Logger) *HTTPNotifier {
	return &HTTPNotifier{httpClient: httpClient, logger: logger}
}

type payload struct {
	Text        string       `json:"text"`
	Channel     string       `json:"channel,omitempty"`
	Attachments []attachment `json:"attachments,omitempty"`
}

type attachment struct {
	Title     string  `json:"title,omitempty"`
	TitleLink string  `json:"title_link,omitempty"`
	Fields    []field `json:"fields,omitempty"`
}

type field struct {
	Short bool   `json:"short"`
	Title string `json:"title"`
	Value string `json:"value"`
}

// Send posts m to channel, and again to "@<DirectTo>" when DirectTo is set.
// A failed direct message does not stop the channel message (or the other way
// round); the errors are joined.
func (n *HTTPNotifier) Send(ctx context.Context, webhookURL, channel string, m application.NotificationMessage) error {
	body := buildPayload(m)
	errs := []error{}
	// An empty channel posts to the webhook's own default channel.
	body.Channel = channel
	if err := n.post(ctx, webhookURL, body); err != nil {
		errs = append(errs, err)
	}
	if m.DirectTo != "" {
		body.Channel = "@" + m.DirectTo
		if err := n.post(ctx, webhookURL, body); err != nil {
			errs = append(errs, fmt.Errorf("direct message to %s: %w", m.DirectTo, err))
		}
	}
	return errors.Join(errs...)
}

// Test sends a fixed message for the admin "Send test" button.
func (n *HTTPNotifier) Test(ctx context.Context, webhookURL, channel string) error {
	return n.Send(ctx, webhookURL, channel, application.NotificationMessage{
		Title:  "Tiphys test notification",
		Fields: []application.NotificationField{{Name: "What", Value: "Webhook test from the admin settings"}},
	})
}

func buildPayload(m application.NotificationMessage) payload {
	p := payload{Text: m.Title}
	if len(m.Fields) == 0 && m.Link == "" {
		return p
	}
	a := attachment{TitleLink: m.Link}
	if m.Link != "" {
		a.Title = "Open in Tiphys"
	}
	for _, f := range m.Fields {
		// Short fields render two per row, which suits who/where/environment.
		a.Fields = append(a.Fields, field{Short: len(f.Value) <= 40, Title: f.Name, Value: f.Value})
	}
	p.Attachments = []attachment{a}
	return p
}

func (n *HTTPNotifier) post(ctx context.Context, webhookURL string, p payload) error {
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("failed to encode rocket.chat message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(data))
	if err != nil {
		// The URL contains the webhook token, so it is never logged.
		n.logger.Error("failed to build rocket.chat request", zap.Error(err))
		return errors.New("failed to build rocket.chat request: invalid webhook url")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		n.logger.Error("rocket.chat call failed", zap.String("channel", p.Channel), zap.Error(err))
		return fmt.Errorf("rocket.chat call failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, logBodyLimit))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		n.logger.Error("rocket.chat returned non-2xx", zap.String("channel", p.Channel), zap.Int("status", resp.StatusCode), zap.String("body", string(respBody)))
		return fmt.Errorf("rocket.chat returned status %d", resp.StatusCode)
	}
	return nil
}
