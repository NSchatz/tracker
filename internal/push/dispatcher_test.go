package push

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fcmDelivery(token string) Delivery {
	return Delivery{Sub: Subscription{Provider: PushProviderKeyFCM, Token: token}}
}

// recordingSender records every delivery it is asked to send onto a buffered channel and always
// succeeds — the happy-path stub for routing/delivery assertions.
type recordingSender struct{ ch chan Delivery }

func (r *recordingSender) Send(_ context.Context, d Delivery) error {
	r.ch <- d
	return nil
}

// TestDispatcherRetriesWithinLimits proves a delivery that fails transiently is retried and then
// succeeds — "a push-send failure is retried within limits" (§5.3). The sender fails its first two
// attempts and succeeds on the third; with maxAttempts=3 the delivery lands.
func TestDispatcherRetriesWithinLimits(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int64
	done := make(chan struct{})
	sender := senderFunc(func(context.Context, Delivery) error {
		if attempts.Add(1) < 3 {
			return errors.New("transient")
		}
		close(done)
		return nil
	})

	d := NewDispatcher(map[string]Sender{PushProviderKeyFCM: sender}, testLogger(),
		WithMaxAttempts(3), WithRetryBackoff(0))
	defer d.Close()

	d.Enqueue(fcmDelivery("tok"))

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("delivery never succeeded within its retry budget")
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want exactly 3 (two failures then a success)", got)
	}
}

// TestDispatcherDropsAfterMaxAttemptsWithoutCrashing proves a persistently-failing delivery is given up
// on after its budget and LOGGED, not crashed (§6 "logged, not crashed"). The worker must survive and
// keep serving the next delivery.
func TestDispatcherDropsAfterMaxAttemptsWithoutCrashing(t *testing.T) {
	t.Parallel()

	var failCalls atomic.Int64
	failing := senderFunc(func(context.Context, Delivery) error {
		failCalls.Add(1)
		return errors.New("permanent")
	})
	ok := &recordingSender{ch: make(chan Delivery, 1)}

	d := NewDispatcher(map[string]Sender{PushProviderKeyFCM: failing, PushProviderKeyUnifiedPush: ok},
		testLogger(), WithMaxAttempts(2), WithRetryBackoff(0))
	defer d.Close()

	// A doomed delivery, then a good one behind it.
	d.Enqueue(fcmDelivery("doomed"))
	d.Enqueue(Delivery{Sub: Subscription{Provider: PushProviderKeyUnifiedPush, Token: "up"}})

	select {
	case got := <-ok.ch:
		if got.Sub.Token != "up" {
			t.Errorf("delivered %q, want the second delivery — the worker did not survive the failure", got.Sub.Token)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the worker did not serve the delivery after a permanent failure — it may have died")
	}
	if got := failCalls.Load(); got != 2 {
		t.Errorf("failing delivery attempted %d times, want exactly maxAttempts=2", got)
	}
}

// TestDispatcherPendingCapDropsRatherThanBlocks proves the 100-pending cap (here 4): when the worker is
// stuck and the queue fills, Enqueue DROPS rather than blocking — the property that keeps a stalled
// push backend from ever blocking ingestion.
func TestDispatcherPendingCapDropsRatherThanBlocks(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	blocking := senderFunc(func(context.Context, Delivery) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil
	})

	d := NewDispatcher(map[string]Sender{PushProviderKeyFCM: blocking}, testLogger(),
		WithMaxPending(4), WithRetryBackoff(0))

	// The worker pulls the first delivery and blocks inside Send.
	d.Enqueue(fcmDelivery("blocker"))
	<-entered

	// With the worker stuck, the queue (depth 4) fills and the rest are dropped. Enqueue must never
	// block, so this whole loop returns promptly.
	for i := 0; i < 20; i++ {
		d.Enqueue(fcmDelivery("overflow"))
	}
	if d.Dropped() == 0 {
		t.Fatal("nothing was dropped with a full queue and a stuck worker — Enqueue must have blocked")
	}
	if d.Dropped() != 16 {
		t.Errorf("dropped %d, want 16 (20 offered, 4 buffered)", d.Dropped())
	}

	close(release) // let the worker drain
	d.Close()
}

// TestDispatcherRoutesByProvider proves each delivery goes to the sender registered for its provider,
// and a delivery for an unconfigured provider is dropped (logged) rather than crashing the worker.
func TestDispatcherRoutesByProvider(t *testing.T) {
	t.Parallel()

	fcm := &recordingSender{ch: make(chan Delivery, 4)}
	// Only fcm is configured; a unifiedpush delivery has no sender.
	d := NewDispatcher(map[string]Sender{PushProviderKeyFCM: fcm}, testLogger(), WithRetryBackoff(0))
	defer d.Close()

	d.Enqueue(Delivery{Sub: Subscription{Provider: PushProviderKeyUnifiedPush, Token: "no-sender"}})
	d.Enqueue(fcmDelivery("routed"))

	select {
	case got := <-fcm.ch:
		if got.Sub.Token != "routed" {
			t.Errorf("fcm sender got %q, want the fcm delivery (the unconfigured one must be skipped)", got.Sub.Token)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fcm delivery never arrived — an unconfigured-provider delivery may have killed the worker")
	}
}

// senderFunc adapts a function to the Sender interface for the tests.
type senderFunc func(context.Context, Delivery) error

func (f senderFunc) Send(ctx context.Context, d Delivery) error { return f(ctx, d) }
