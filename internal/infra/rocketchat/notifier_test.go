package rocketchat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"tiphys/internal/application"
)

type recorder struct {
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
	status func(channel string) int
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	r.mu.Lock()
	r.bodies = append(r.bodies, m)
	r.paths = append(r.paths, req.URL.Path)
	r.mu.Unlock()
	ch, _ := m["channel"].(string)
	status := http.StatusOK
	if r.status != nil {
		status = r.status(ch)
	}
	w.WriteHeader(status)
}

func newNotifier(t *testing.T, h http.Handler) (*HTTPNotifier, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewHTTPNotifier(srv.Client(), zap.NewNop()), srv.URL + "/hooks/abc/token"
}

func TestSendChannelOnly(t *testing.T) {
	rec := &recorder{}
	n, url := newNotifier(t, rec)
	err := n.Send(context.Background(), url, "#alerts", application.NotificationMessage{
		Title:  "Break-glass started",
		Fields: []application.NotificationField{{Name: "Who", Value: "alice"}, {Name: "Reason", Value: strings.Repeat("long ", 20)}},
		Link:   "https://tiphys.example/break-glass/3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.bodies) != 1 || rec.paths[0] != "/hooks/abc/token" {
		t.Fatalf("calls = %v %v", rec.paths, rec.bodies)
	}
	b := rec.bodies[0]
	if b["text"] != "Break-glass started" || b["channel"] != "#alerts" {
		t.Errorf("body = %v", b)
	}
	att := b["attachments"].([]any)[0].(map[string]any)
	if att["title_link"] != "https://tiphys.example/break-glass/3" || att["title"] == "" {
		t.Errorf("attachment = %v", att)
	}
	fields := att["fields"].([]any)
	if len(fields) != 2 || fields[0].(map[string]any)["title"] != "Who" || fields[0].(map[string]any)["short"] != true || fields[1].(map[string]any)["short"] != false {
		t.Errorf("fields = %v", fields)
	}
}

func TestSendDirect(t *testing.T) {
	rec := &recorder{}
	n, url := newNotifier(t, rec)
	err := n.Send(context.Background(), url, "#alerts", application.NotificationMessage{Title: "Decided", DirectTo: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.bodies) != 2 || rec.bodies[0]["channel"] != "#alerts" || rec.bodies[1]["channel"] != "@bob" {
		t.Fatalf("bodies = %v", rec.bodies)
	}
	if _, ok := rec.bodies[0]["attachments"]; ok {
		t.Error("no fields or link: attachments should be omitted")
	}
}

func TestSendErrors(t *testing.T) {
	rec := &recorder{status: func(ch string) int {
		if ch == "@bob" {
			return http.StatusBadRequest
		}
		return http.StatusOK
	}}
	n, url := newNotifier(t, rec)
	err := n.Send(context.Background(), url, "#alerts", application.NotificationMessage{Title: "x", DirectTo: "bob"})
	if err == nil || !strings.Contains(err.Error(), "direct message to bob") {
		t.Fatalf("err = %v, want failed DM", err)
	}
	if len(rec.bodies) != 2 {
		t.Errorf("channel message should still be sent, calls = %d", len(rec.bodies))
	}

	n, url = newNotifier(t, &recorder{status: func(string) int { return http.StatusInternalServerError }})
	if err := n.Send(context.Background(), url, "#a", application.NotificationMessage{Title: "x"}); err == nil {
		t.Error("500: want error")
	}
	// 2xx other than 200 is success.
	n, url = newNotifier(t, &recorder{status: func(string) int { return http.StatusAccepted }})
	if err := n.Send(context.Background(), url, "#a", application.NotificationMessage{Title: "x"}); err != nil {
		t.Errorf("202: %v", err)
	}
	if err := n.Send(context.Background(), "http://127.0.0.1:1/x", "#a", application.NotificationMessage{Title: "x"}); err == nil {
		t.Error("unreachable: want error")
	}
}

func TestTest(t *testing.T) {
	rec := &recorder{}
	n, url := newNotifier(t, rec)
	if err := n.Test(context.Background(), url, "#ops"); err != nil {
		t.Fatal(err)
	}
	if len(rec.bodies) != 1 || rec.bodies[0]["channel"] != "#ops" || !strings.Contains(rec.bodies[0]["text"].(string), "test") {
		t.Errorf("bodies = %v", rec.bodies)
	}
}
