package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUnifiedPushSenderParity is the roadmap's §6 "UnifiedPush sender parity test": the degoogled
// backend delivers the SAME alert content as FCM — same title, body, high priority, collapse key and
// data — to the subscription's endpoint URL. A family on either backend gets an identical notification;
// that parity is the whole reason S6 supports both.
func TestUnifiedPushSenderParity(t *testing.T) {
	t.Parallel()

	var (
		gotMethod, gotCT, gotUrgency string
		gotBody                      []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotUrgency = r.Header.Get("Urgency")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// The subscription's token IS the endpoint URL for UnifiedPush.
	d := sampleDelivery()
	d.Sub = Subscription{Provider: PushProviderKeyUnifiedPush, Token: srv.URL + "/UP-endpoint"}

	sender := NewUnifiedPushSender(nil)
	if err := sender.Send(context.Background(), d); err != nil {
		t.Fatalf("UnifiedPush Send: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if gotUrgency != "high" {
		t.Errorf("Urgency header = %q, want high", gotUrgency)
	}

	var msg struct {
		Title       string            `json:"title"`
		Body        string            `json:"body"`
		Priority    string            `json:"priority"`
		CollapseKey string            `json:"collapse_key"`
		Data        map[string]string `json:"data"`
	}
	if err := json.Unmarshal(gotBody, &msg); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, gotBody)
	}
	// Parity with what FCM carries (sampleDelivery is the same delivery both senders are tested on).
	if msg.Title != "Alice's phone" || msg.Body != "Alice's phone arrived at School" {
		t.Errorf("title/body = %q/%q, want parity with the delivery", msg.Title, msg.Body)
	}
	if msg.Priority != "high" {
		t.Errorf("priority = %q, want high", msg.Priority)
	}
	if msg.CollapseKey != "gf:dev-1:place-1" {
		t.Errorf("collapse_key = %q, want the delivery's collapse key", msg.CollapseKey)
	}
	if msg.Data["transition"] != "enter" || msg.Data["place_name"] != "School" {
		t.Errorf("data = %v, want the delivery's data map", msg.Data)
	}
}

// A non-2xx from the distributor is an error, mirroring the FCM sender — the dispatcher treats both the
// same.
func TestUnifiedPushSenderReportsNon2xxAsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("distributor down"))
	}))
	defer srv.Close()

	d := sampleDelivery()
	d.Sub = Subscription{Provider: PushProviderKeyUnifiedPush, Token: srv.URL}

	err := NewUnifiedPushSender(nil).Send(context.Background(), d)
	if err == nil {
		t.Fatal("Send returned nil for a 503, want an error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %q, want it to carry the status", err)
	}
}
