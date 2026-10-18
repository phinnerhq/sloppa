package password

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	_ "modernc.org/sqlite"
)

func mustHash(t *testing.T, password string) string {
	t.Helper()
	h, err := Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func verifies(t *testing.T, password, hash string) bool {
	t.Helper()
	ok, err := Verify(t.Context(), password, hash)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestRoundTrip(t *testing.T) {
	h := mustHash(t, "correct horse")
	if !verifies(t, "correct horse", h) || verifies(t, "wrong", h) {
		t.Fatal("round trip failed")
	}
}

func TestRejectsGarbage(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2id$v=19$m=99999999,t=2,p=1$YQ$YQ", "$argon2id$v=19$m=65536,t=0,p=1$YQ$YQ"} {
		if verifies(t, "x", h) {
			t.Errorf("%q verified", h)
		}
	}
}

// A flood of sign-ins mustn't each get 64 MiB: past the slots and a short queue, they're refused at
// once, and those queued give up after a while.
func TestBudgetBoundsHashesAndTheirQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newBudget(1, 1, time.Second)
		release, err := b.acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		queued := make(chan error)
		go func() {
			r, err := b.acquire(t.Context())
			if err == nil {
				r()
			}
			queued <- err
		}()
		synctest.Wait()
		if _, err := b.acquire(t.Context()); !errors.Is(err, ErrBusy) {
			t.Fatalf("past the queue: %v", err)
		}
		release()
		if err := <-queued; err != nil {
			t.Fatalf("queued: %v", err)
		}

		release, _ = b.acquire(t.Context())
		start := time.Now()
		if _, err := b.acquire(t.Context()); !errors.Is(err, ErrBusy) || time.Since(start) != time.Second {
			t.Fatalf("queued for %v: %v", time.Since(start), err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := b.acquire(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled: %v", err)
		}
		release()
		if b.waiting.Load() != 0 {
			t.Fatalf("%d still counted as waiting", b.waiting.Load())
		}
	})
}

func TestHashAndVerifyShareTheBudget(t *testing.T) {
	h := mustHash(t, "correct horse")
	for range cap(shared.slots) {
		shared.slots <- struct{}{}
	}
	shared.waiting.Add(shared.queue)
	defer func() {
		shared.waiting.Add(-shared.queue)
		for range cap(shared.slots) {
			<-shared.slots
		}
	}()
	if _, err := Verify(t.Context(), "correct horse", h); !errors.Is(err, ErrBusy) {
		t.Errorf("verify: %v", err)
	}
	if _, err := Hash(t.Context(), "x"); !errors.Is(err, ErrBusy) {
		t.Errorf("hash: %v", err)
	}
	// Garbage is turned away without a slot.
	if ok, err := Verify(t.Context(), "x", "plain"); ok || err != nil {
		t.Errorf("garbage: %v %v", ok, err)
	}
}
