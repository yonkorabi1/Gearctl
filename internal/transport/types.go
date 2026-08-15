// Package transport implements the SSH/Telnet session layer: opening a
// connection, completing any login sequence, and running commands against
// an interactive shell with vendor-agnostic prompt-return detection.
package transport

// ConnectionParams mirrors the original TypeScript ConnectionParams shape.
// Empty string means "not set" for the optional fields (EnablePassword,
// DisablePagingCommand) -- Go has no natural `undefined`, and the JSON
// decoder in httpapi already normalizes a missing/null field to "".
type ConnectionParams struct {
	Host                 string
	Port                 int
	Protocol             string // "ssh" | "telnet"
	Vendor               string // free-text hint from the `olts` table; see enablevendors.go
	Username             string
	Password             string
	EnablePassword       string
	ConnectTimeoutMs     int
	PerCommandTimeoutMs  int
	DisablePagingCommand string
}

// CommandResult mirrors the TypeScript CommandResult shape for JSON
// round-tripping through httpapi. Output is a pointer so a failed command
// serializes as `"output": null`, matching the original contract exactly.
type CommandResult struct {
	Command    string  `json:"command"`
	Output     *string `json:"output"`
	DurationMs int64   `json:"durationMs"`
	Truncated  bool    `json:"truncated"`
	Error      string  `json:"error,omitempty"` // "timeout" | "session_closed"
}

// MaxOutputBytes caps captured output per command before truncating -- see README.md.
const MaxOutputBytes = 256 * 1024

// Default timeouts, shared by every caller of ConnectionParams -- the HTTP
// API (httpapi.ExecuteRequest/FactsRequest, for a caller that omits the
// field) and the standalone CLI mode (main.go's flag defaults).
const (
	DefaultConnectTimeoutMs    = 10_000
	DefaultPerCommandTimeoutMs = 15_000
)

// DataSource is the minimal byte-stream abstraction both SSH and Telnet
// transports feed into the shared prompt-detection logic (promptdetector.go).
// Chunks must never be dropped -- a buffered channel fed by one dedicated
// reader goroutine per connection, rather than a fan-out "listener list"
// (the original TS DataSource's shape), naturally preserves every byte for
// whichever stage (capturePrompt, then each waitForPrompt call in turn) is
// reading at the time.
type DataSource struct {
	Chunks chan []byte
	Write  func(data string) error
}
