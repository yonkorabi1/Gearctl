// Package sessionpool is gearctl's in-memory pool of open,
// already-authenticated device connections -- the "one session, many
// commands, across separate requests" feature (see gearctl's README,
// "Session pooling"). HTTP-server-mode only: a CLI invocation is its own
// OS process and has no memory that outlives it for a pool to live in,
// so this package is only ever wired into internal/httpapi, never into
// main.go's CLI path.
//
// The design choice this package embodies, deliberately, over the
// obvious alternative: a Session is keyed by a fresh, high-entropy random
// ID (crypto/rand, never math/rand), NOT by a hash of the caller's
// credentials. The credentials themselves are used exactly once, at
// Store.Open time, to establish the connection -- and are never touched,
// stored, or compared again anywhere in this package. Reusing the
// session afterward requires only the ID (a bearer-token-style
// capability: whoever holds it can drive the connection until it's
// closed or reaped), not the original password. This is what lets
// gearctl offer connection reuse across separate requests without
// resurrecting the "cache something derived from the password in
// memory" tradeoff a transparent credential-hash pool would require.
package sessionpool

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/onu-platform/gearctl/internal/drivers"
)

// ErrNotFound is returned by Acquire/Close when id doesn't match any
// currently-open session -- it never existed, was already closed, or was
// reaped for sitting idle longer than the store's IdleTimeout.
var ErrNotFound = errors.New("session not found")

// ErrBusy is returned by Acquire when id exists but is currently locked
// by another in-flight operation and doesn't free up within the timeout
// passed to Acquire. A device CLI session cannot safely run two commands
// at once (there is exactly one prompt to read), so this is a real
// constraint, not an arbitrary one -- see Session's lock field.
var ErrBusy = errors.New("session busy")

// Session is one pooled, already-connected device driver. Callers never
// construct one directly -- see Store.Open.
type Session struct {
	ID          string
	Driver      drivers.CliDriver
	Vendor      string
	ConnectedAt time.Time

	// lock is a capacity-1 channel used as a try-lock-with-timeout mutex:
	// a full buffer slot (one token present) means "available", an empty
	// buffer means "held". Acquire receives the token (locks), Release
	// sends it back (unlocks). This -- not sync.Mutex -- is what makes a
	// bounded-wait "try to acquire, give up after timeout" possible
	// without leaking a goroutine still waiting on a lock nobody will
	// ever release-and-then-abandon: a sync.Mutex has no way to stop
	// waiting on Lock() once called, so a goroutine spawned to call it
	// and then abandoned on timeout would still eventually acquire it
	// and hold it forever. Selecting on a channel receive has no such
	// problem -- giving up just means not receiving, and the token
	// remains for whoever holds the lock to return.
	lock chan struct{}

	// lastUsedAt is read/written only by Store, under Store.mu -- never
	// accessed directly by Session's own methods, to keep exactly one
	// lock protecting it.
	lastUsedAt time.Time
}

func newSession(id string, driver drivers.CliDriver, vendor string, now time.Time) *Session {
	lock := make(chan struct{}, 1)
	lock <- struct{}{} // starts unlocked (token present)
	return &Session{
		ID:          id,
		Driver:      driver,
		Vendor:      vendor,
		ConnectedAt: now,
		lock:        lock,
		lastUsedAt:  now,
	}
}

// tryLock attempts to acquire sess's lock, waiting up to timeout. Returns
// false on timeout -- in that case nothing was consumed, so the actual
// holder's eventual unlock() still correctly makes the token available
// for the next attempt (see the lock field's doc comment for why this
// matters).
func (sess *Session) tryLock(timeout time.Duration) bool {
	select {
	case <-sess.lock:
		return true
	case <-time.After(timeout):
		return false
	}
}

// unlock returns the token. Safe to call even if, somehow, nothing is
// waiting -- the send is non-blocking (the channel has room for exactly
// the one token that was always either in the channel or held by a
// caller, never both, so this branch is defensive, not expected to fire
// in correct usage).
func (sess *Session) unlock() {
	select {
	case sess.lock <- struct{}{}:
	default:
	}
}

// Store is the process-wide session pool. One Store per gearctl server
// process (see internal/httpapi's package-level sessionStore variable
// for why it's a singleton, not one per request or per test).
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session

	idleTimeout time.Duration
	stop        chan struct{}
	stopped     bool
}

// NewStore creates a Store and starts its background idle-reaper
// goroutine immediately -- call Stop when the store is no longer needed
// (tests that create many short-lived Stores should do this; the one
// package-level Store a real server process creates lives for the
// process's whole lifetime and never needs to).
func NewStore(idleTimeout time.Duration) *Store {
	s := &Store{
		sessions:    make(map[string]*Session),
		idleTimeout: idleTimeout,
		stop:        make(chan struct{}),
	}
	go s.reapLoop()
	return s
}

// newSessionID generates a 256-bit random, URL-safe token. crypto/rand,
// never math/rand -- this is the one piece of data that stands in for
// "proof you're allowed to use this connection," so it needs to be
// unguessable, not just distinct.
func newSessionID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Open registers an already-connected driver under a new random ID and
// returns the Session. The caller must have already called
// driver.Connect() successfully -- Store itself never dials anything and
// never sees the credentials that were used to do so, keeping this
// package decoupled from transport-level and credential concerns
// entirely (see the package doc comment).
func (s *Store) Open(driver drivers.CliDriver, vendor string) (*Session, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, fmt.Errorf("session store is shut down")
	}
	sess := newSession(id, driver, vendor, time.Now())
	s.sessions[id] = sess
	return sess, nil
}

// Acquire looks up id and locks it for exclusive use, waiting up to
// timeout for a concurrent operation on the SAME session to finish first.
// Returns ErrBusy if timeout elapses first, ErrNotFound if id doesn't
// exist (never existed, already closed, or reaped for idling). Callers
// MUST call Release(sess) when done, success or failure alike -- the same
// defer-immediately convention every handler already uses for
// driver.Disconnect() on the fresh-connection path.
func (s *Store) Acquire(id string, timeout time.Duration) (*Session, error) {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}

	if !sess.tryLock(timeout) {
		return nil, ErrBusy
	}
	return sess, nil
}

// Release marks sess as no longer in use: unlocks it for the next
// Acquire, and resets its idle clock (activity, whether the operation
// that just ran succeeded or failed, is still activity -- it postpones
// idle reaping either way).
func (s *Store) Release(sess *Session) {
	s.mu.Lock()
	sess.lastUsedAt = time.Now()
	s.mu.Unlock()
	sess.unlock()
}

// Close removes id from the store and disconnects its driver. Returns
// ErrNotFound if id doesn't exist. Waits briefly for any in-flight
// operation to finish before disconnecting (so an explicit close doesn't
// yank the connection out from under a command that's still running),
// but doesn't wait forever -- an explicit close request should win
// eventually even if something is stuck.
func (s *Store) Close(id string) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}

	sess.tryLock(closeLockWait)
	return sess.Driver.Disconnect()
}

// closeLockWait bounds how long Close/the idle reaper wait for a
// session's lock before disconnecting anyway -- long enough to let a
// command that's about to finish do so cleanly, short enough that a
// genuinely stuck operation never blocks a close/reap indefinitely.
const closeLockWait = 2 * time.Second

func (s *Store) reapLoop() {
	ticker := time.NewTicker(idleReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.reapIdle()
		case <-s.stop:
			return
		}
	}
}

// idleReapInterval is how often the reaper checks for idle sessions --
// deliberately finer-grained than any reasonable IdleTimeout, so a
// session is reaped soon after crossing the threshold, not up to a full
// extra interval late.
const idleReapInterval = 5 * time.Second

func (s *Store) reapIdle() {
	now := time.Now()
	s.mu.Lock()
	var toClose []*Session
	for id, sess := range s.sessions {
		if now.Sub(sess.lastUsedAt) >= s.idleTimeout {
			toClose = append(toClose, sess)
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()

	for _, sess := range toClose {
		sess.tryLock(closeLockWait)
		_ = sess.Driver.Disconnect()
	}
}

// Stop halts the idle-reaper goroutine. Safe to call more than once.
// Does NOT close any still-open sessions -- callers that want a clean
// shutdown should Close each one first. Exists primarily so tests that
// create many short-lived Stores don't each leak a goroutine; the one
// Store a real gearctl server process creates lives for the process's
// entire lifetime and has no occasion to call this.
func (s *Store) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		s.stopped = true
		close(s.stop)
	}
}

// Len reports how many sessions are currently open -- test/diagnostic
// use only (e.g. confirming idle reaping actually removed an entry).
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}
