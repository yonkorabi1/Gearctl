package httpapi

import (
	"errors"
	"fmt"
	"time"

	"github.com/onu-platform/gearctl/internal/sessionpool"
)

// sessionIdleTimeout: how long a pooled session may sit with no activity
// before gearctl closes it on its own. See gearctl's README ("Session
// pooling") for the rationale behind this specific value.
const sessionIdleTimeout = 30 * time.Second

// sessionAcquireTimeout: how long a request waits for a session that's
// currently busy with another operation before giving up and reporting
// "session_busy" -- a device CLI session can only run one command at a
// time (see sessionpool.Session's doc comment), so a second request
// arriving mid-operation has to wait its turn or fail fast; this bounds
// how long it waits before failing fast.
const sessionAcquireTimeout = 5 * time.Second

// sessionStore is the one process-wide session pool for this gearctl
// server. A package-level singleton, not one per NewServer() call or per
// request, for two reasons: there's only ever one gearctl server process
// in practice (see gearctl's README, Architecture), and creating a fresh
// Store per NewServer() call would spin up a fresh idle-reaper goroutine
// every time -- harmless in production (NewServer is called once) but
// wasteful across this package's ~30 handler tests, each of which calls
// NewServer independently.
var sessionStore = sessionpool.NewStore(sessionIdleTimeout)

// acquireSession looks up and locks sessionID for exclusive use,
// translating sessionpool's errors into the ExecuteError shape every
// handler's response already uses. Callers MUST call
// sessionStore.Release(sess) when done (defer it immediately, success or
// failure alike) -- the session-based counterpart to every handler's
// existing `defer driver.Disconnect()` on the fresh-connection path,
// except this only releases a lock; the connection itself stays open for
// the next request.
func acquireSession(sessionID string) (*sessionpool.Session, *ExecuteError) {
	sess, err := sessionStore.Acquire(sessionID, sessionAcquireTimeout)
	if err != nil {
		if errors.Is(err, sessionpool.ErrNotFound) {
			return nil, &ExecuteError{
				Type:    "session_not_found",
				Message: fmt.Sprintf("no open session %q -- it may have expired (idle > %s) or already been closed", sessionID, sessionIdleTimeout),
			}
		}
		return nil, &ExecuteError{
			Type:    "session_busy",
			Message: fmt.Sprintf("session %q is busy with another operation, try again shortly", sessionID),
		}
	}
	return sess, nil
}
