package httpapi

import (
	"github.com/onu-platform/olt-cli-gateway/internal/transport"
)

type Credentials struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	EnablePassword string `json:"enablePassword,omitempty"`
}

// ExecuteRequest mirrors the original TypeScript ExecuteRequestSchema
// (zod) field-for-field. ConnectTimeoutMs/PerCommandTimeoutMs are pointers
// so Validate/the handler can distinguish "omitted" (use the default) from
// an explicit value.
type ExecuteRequest struct {
	Host                 string      `json:"host"`
	Port                 int         `json:"port"`
	Protocol             string      `json:"protocol"`
	Vendor               string      `json:"vendor,omitempty"`
	Credentials          Credentials `json:"credentials"`
	Commands             []string    `json:"commands"`
	DisablePagingCommand string      `json:"disablePagingCommand,omitempty"`
	ConnectTimeoutMs     *int        `json:"connectTimeoutMs,omitempty"`
	PerCommandTimeoutMs  *int        `json:"perCommandTimeoutMs,omitempty"`
}

const (
	DefaultConnectTimeoutMs    = 10_000
	DefaultPerCommandTimeoutMs = 15_000
)

// Validate returns a human-readable message describing the first
// violation found, or "" if the request is valid.
func (r *ExecuteRequest) Validate() string {
	if r.Host == "" {
		return "host is required"
	}
	if r.Port < 1 || r.Port > 65535 {
		return "port must be between 1 and 65535"
	}
	if r.Protocol != "ssh" && r.Protocol != "telnet" {
		return "protocol must be \"ssh\" or \"telnet\""
	}
	if r.Credentials.Username == "" {
		return "credentials.username is required"
	}
	if r.Credentials.Password == "" {
		return "credentials.password is required"
	}
	if len(r.Commands) == 0 {
		return "commands must have at least 1 entry"
	}
	if len(r.Commands) > 50 {
		return "commands must have at most 50 entries"
	}
	for _, c := range r.Commands {
		if c == "" {
			return "commands entries must not be empty"
		}
	}
	if r.ConnectTimeoutMs != nil && (*r.ConnectTimeoutMs < 1 || *r.ConnectTimeoutMs > 60_000) {
		return "connectTimeoutMs must be between 1 and 60000"
	}
	if r.PerCommandTimeoutMs != nil && (*r.PerCommandTimeoutMs < 1 || *r.PerCommandTimeoutMs > 120_000) {
		return "perCommandTimeoutMs must be between 1 and 120000"
	}
	return ""
}

func (r *ExecuteRequest) ToConnectionParams() transport.ConnectionParams {
	connectTimeoutMs := DefaultConnectTimeoutMs
	if r.ConnectTimeoutMs != nil {
		connectTimeoutMs = *r.ConnectTimeoutMs
	}
	perCommandTimeoutMs := DefaultPerCommandTimeoutMs
	if r.PerCommandTimeoutMs != nil {
		perCommandTimeoutMs = *r.PerCommandTimeoutMs
	}
	return transport.ConnectionParams{
		Host:                 r.Host,
		Port:                 r.Port,
		Protocol:             r.Protocol,
		Vendor:               r.Vendor,
		Username:             r.Credentials.Username,
		Password:             r.Credentials.Password,
		EnablePassword:       r.Credentials.EnablePassword,
		ConnectTimeoutMs:     connectTimeoutMs,
		PerCommandTimeoutMs:  perCommandTimeoutMs,
		DisablePagingCommand: r.DisablePagingCommand,
	}
}

type ExecuteError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ExecuteResponse mirrors the original TypeScript ExecuteResponse shape
// exactly -- platform-api's OltCliService JSON-decodes this without any
// changes regardless of which language implements the gateway.
type ExecuteResponse struct {
	Status          string                    `json:"status"`
	Results         []transport.CommandResult `json:"results"`
	Error           *ExecuteError             `json:"error"`
	ConnectedAt     *string                   `json:"connectedAt"`
	TotalDurationMs int64                     `json:"totalDurationMs"`
}
