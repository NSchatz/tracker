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

// A geofence delivery the sender tests reuse: it exercises title, body, collapse key and data at once.
func sampleDelivery() Delivery {
	return Delivery{
		Sub: Subscription{Provider: PushProviderKeyFCM, Token: "device-registration-token"},
		Note: Notification{
			Title:       "Alice's phone",
			Body:        "Alice's phone arrived at School",
			CollapseKey: "gf:dev-1:place-1",
			Data:        map[string]string{"transition": "enter", "place_name": "School"},
		},
	}
}

// PushProviderKeyFCM/UnifiedPush mirror the store's provider constants without importing store into the
// push package's tests — the values are the contract, asserted equal to store's in store's own tests.
const (
	PushProviderKeyFCM         = "fcm"
	PushProviderKeyUnifiedPush = "unifiedpush"
)

// TestFCMSenderRequestShape pins the FCM HTTP v1 request the roadmap's §6 names: the v1 path, a
// high-priority AndroidConfig, the collapse key, the bearer token from the token source, and the
// message body addressed to the subscription's token — all asserted against a mock FCM (roadmap §10
// open question #5: verify the exact request against a stand-in before trusting it).
func TestFCMSenderRequestShape(t *testing.T) {
	t.Parallel()

	var (
		gotMethod, gotPath, gotAuth, gotCT string
		gotBody                            []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"projects/test-project/messages/123"}`))
	}))
	defer srv.Close()

	sender := NewFCMSender("test-project", StaticTokenSource("EXAMPLE-access-token"), nil)
	sender.baseURL = srv.URL // point at the mock instead of googleapis.com

	if err := sender.Send(context.Background(), sampleDelivery()); err != nil {
		t.Fatalf("Send to mock FCM: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if want := "/v1/projects/test-project/messages:send"; gotPath != want {
		t.Errorf("path = %s, want %s", gotPath, want)
	}
	if want := "Bearer EXAMPLE-access-token"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}

	var req struct {
		Message struct {
			Token        string                       `json:"token"`
			Notification struct{ Title, Body string } `json:"notification"`
			Data         map[string]string            `json:"data"`
			Android      struct {
				Priority    string `json:"priority"`
				CollapseKey string `json:"collapse_key"`
			} `json:"android"`
		} `json:"message"`
	}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("request body not the v1 shape: %v (%s)", err, gotBody)
	}
	m := req.Message
	if m.Token != "device-registration-token" {
		t.Errorf("message.token = %q, want the subscription token", m.Token)
	}
	if m.Android.Priority != "high" {
		t.Errorf("android.priority = %q, want high (only high priority wakes a closed app)", m.Android.Priority)
	}
	if m.Android.CollapseKey != "gf:dev-1:place-1" {
		t.Errorf("android.collapse_key = %q, want the notification's collapse key", m.Android.CollapseKey)
	}
	if m.Notification.Title != "Alice's phone" || m.Notification.Body != "Alice's phone arrived at School" {
		t.Errorf("notification = %+v, want the delivery's title/body", m.Notification)
	}
	if m.Data["transition"] != "enter" || m.Data["place_name"] != "School" {
		t.Errorf("data = %v, want the delivery's data map", m.Data)
	}
}

// A non-2xx from FCM is an error the dispatcher can act on, carrying FCM's reason so the failure log is
// actionable (§6 delivery-failure handling).
func TestFCMSenderReportsNon2xxAsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	sender := NewFCMSender("test-project", StaticTokenSource("tok"), nil)
	sender.baseURL = srv.URL

	err := sender.Send(context.Background(), sampleDelivery())
	if err == nil {
		t.Fatal("Send returned nil for a 400 from FCM, want an error")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
		t.Errorf("error = %q, want it to carry the status and FCM's reason", err)
	}
}

// A token source that cannot mint a token fails the send before any request — the dispatcher retries
// and then logs it, exactly as a network failure.
func TestFCMSenderFailsWhenTokenSourceFails(t *testing.T) {
	t.Parallel()

	sender := NewFCMSender("test-project", failingTokenSource{}, nil)
	if err := sender.Send(context.Background(), sampleDelivery()); err == nil {
		t.Fatal("Send returned nil when the token source failed, want an error")
	}
}

type failingTokenSource struct{}

func (failingTokenSource) Token(context.Context) (string, error) {
	return "", io.ErrUnexpectedEOF
}
