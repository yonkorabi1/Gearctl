package sessionpool

import (
	"sync"
	"testing"
	"time"

	"github.com/onu-platform/gearctl/internal/transport"
)

// fakeDriver is a minimal drivers.CliDriver double -- this package never
// dials anything itself (see the package doc comment), so tests only
// need to observe Disconnect() calls, never real connection behavior.
type fakeDriver struct {
	mu              sync.Mutex
	disconnected    bool
	disconnectCount int
}

func (f *fakeDriver) Vendor() string                           { return "fake" }
func (f *fakeDriver) Connect(transport.ConnectionParams) error { return nil }
func (f *fakeDriver) RunCommands([]string) ([]transport.CommandResult, error) {
	return nil, nil
}
func (f *fakeDriver) Disconnect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = true
	f.disconnectCount++
	return nil
}
func (f *fakeDriver) wasDisconnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disconnected
}

func newTestStore(t *testing.T, idleTimeout time.Duration) *Store {
	t.Helper()
	s := NewStore(idleTimeout)
	t.Cleanup(s.Stop)
	return s
}

func TestStoreOpenAcquireRelease(t *testing.T) {
	s := newTestStore(t, time.Minute)
	driver := &fakeDriver{}

	sess, err := s.Open(driver, "cisco")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("Open returned an empty session ID")
	}
	if sess.Vendor != "cisco" {
		t.Errorf("Vendor = %q, want %q", sess.Vendor, "cisco")
	}
	if s.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", s.Len())
	}

	acquired, err := s.Acquire(sess.ID, time.Second)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if acquired != sess {
		t.Error("Acquire returned a different Session than Open did")
	}
	s.Release(acquired)

	// Released -- must be acquirable again immediately.
	if _, err := s.Acquire(sess.ID, time.Second); err != nil {
		t.Fatalf("second Acquire after Release failed: %v", err)
	}
}

func TestStoreAcquireUnknownIDReturnsNotFound(t *testing.T) {
	s := newTestStore(t, time.Minute)
	if _, err := s.Acquire("does-not-exist", 100*time.Millisecond); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// TestStoreAcquireWhileLockedReturnsBusyThenSucceedsAfterRelease is the
// core concurrency-safety test: a device CLI session cannot run two
// commands at once, so a second Acquire while the first is still holding
// the lock must NOT get the session -- and must correctly succeed once
// the first Release happens, proving Release actually frees it for
// reuse rather than leaving it permanently locked.
func TestStoreAcquireWhileLockedReturnsBusyThenSucceedsAfterRelease(t *testing.T) {
	s := newTestStore(t, time.Minute)
	driver := &fakeDriver{}
	sess, err := s.Open(driver, "cisco")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	first, err := s.Acquire(sess.ID, time.Second)
	if err != nil {
		t.Fatalf("first Acquire failed: %v", err)
	}

	if _, err := s.Acquire(sess.ID, 100*time.Millisecond); err != ErrBusy {
		t.Errorf("second concurrent Acquire = %v, want ErrBusy", err)
	}

	s.Release(first)

	if _, err := s.Acquire(sess.ID, time.Second); err != nil {
		t.Errorf("Acquire after Release = %v, want success", err)
	}
}

// TestStoreAcquireTimeoutDoesNotLeakTheLock is the regression test for
// the exact bug a sync.Mutex-plus-goroutine try-lock would have: giving
// up on a timed-out Acquire must NOT consume the lock out from under the
// session -- the actual holder's Release must still correctly unlock it
// for the NEXT caller.
func TestStoreAcquireTimeoutDoesNotLeakTheLock(t *testing.T) {
	s := newTestStore(t, time.Minute)
	driver := &fakeDriver{}
	sess, err := s.Open(driver, "cisco")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	holder, err := s.Acquire(sess.ID, time.Second)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// This one times out and gives up.
	if _, err := s.Acquire(sess.ID, 50*time.Millisecond); err != ErrBusy {
		t.Fatalf("got %v, want ErrBusy", err)
	}

	s.Release(holder)

	// A fresh Acquire must succeed promptly -- if the timed-out attempt
	// above had leaked a goroutine that eventually consumed the token,
	// this would hang or fail.
	done := make(chan error, 1)
	go func() {
		_, err := s.Acquire(sess.ID, time.Second)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Acquire after Release = %v, want success", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire hung -- the timed-out attempt likely leaked the lock")
	}
}

func TestStoreCloseDisconnectsAndRemoves(t *testing.T) {
	s := newTestStore(t, time.Minute)
	driver := &fakeDriver{}
	sess, err := s.Open(driver, "cisco")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	if err := s.Close(sess.ID); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !driver.wasDisconnected() {
		t.Error("Close did not call Disconnect() on the driver")
	}
	if s.Len() != 0 {
		t.Errorf("Len() = %d after Close, want 0", s.Len())
	}
	if _, err := s.Acquire(sess.ID, 100*time.Millisecond); err != ErrNotFound {
		t.Errorf("Acquire after Close = %v, want ErrNotFound", err)
	}
}

func TestStoreCloseUnknownIDReturnsNotFound(t *testing.T) {
	s := newTestStore(t, time.Minute)
	if err := s.Close("does-not-exist"); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// TestStoreIdleReapDisconnectsAfterTimeout is the regression test for the
// idle-timeout feature itself: a session that's opened and never touched
// again must be closed and disconnected on its own, without any explicit
// Close call, once IdleTimeout has elapsed.
func TestStoreIdleReapDisconnectsAfterTimeout(t *testing.T) {
	s := NewStore(50 * time.Millisecond)
	defer s.Stop()
	// Reap check runs every idleReapInterval (5s in production) -- too
	// slow for a test to wait out, so this test drives reapIdle()
	// directly rather than waiting on the real ticker.

	driver := &fakeDriver{}
	sess, err := s.Open(driver, "cisco")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	time.Sleep(60 * time.Millisecond) // older than the 50ms idle timeout
	s.reapIdle()

	if !driver.wasDisconnected() {
		t.Error("idle session was not disconnected by reapIdle")
	}
	if s.Len() != 0 {
		t.Errorf("Len() = %d after reap, want 0", s.Len())
	}
	if _, err := s.Acquire(sess.ID, 100*time.Millisecond); err != ErrNotFound {
		t.Errorf("Acquire after reap = %v, want ErrNotFound", err)
	}
}

// TestStoreReleaseResetsIdleClock confirms activity postpones reaping --
// a session actively being used (Acquire/Release cycling) must NOT be
// reaped just because it was opened a while ago; only genuine inactivity
// since the LAST Release should count.
func TestStoreReleaseResetsIdleClock(t *testing.T) {
	s := NewStore(50 * time.Millisecond)
	defer s.Stop()

	driver := &fakeDriver{}
	sess, err := s.Open(driver, "cisco")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	acquired, err := s.Acquire(sess.ID, time.Second)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	s.Release(acquired) // resets the idle clock to "now"

	s.reapIdle() // idle clock is fresh -- must NOT reap yet
	if driver.wasDisconnected() {
		t.Fatal("session was reaped despite recent activity")
	}

	time.Sleep(60 * time.Millisecond)
	s.reapIdle() // now genuinely idle since the Release above
	if !driver.wasDisconnected() {
		t.Error("session was not reaped after actually going idle")
	}
}

func TestNewSessionIDsAreUniqueAndNonEmpty(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := newSessionID()
		if err != nil {
			t.Fatalf("newSessionID failed: %v", err)
		}
		if id == "" {
			t.Fatal("newSessionID returned an empty string")
		}
		if seen[id] {
			t.Fatalf("newSessionID produced a duplicate: %q", id)
		}
		seen[id] = true
	}
}
