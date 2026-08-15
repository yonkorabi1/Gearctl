package httpapi

import (
	"fmt"

	"github.com/onu-platform/gearctl/internal/drivers"
	"github.com/onu-platform/gearctl/internal/transport"
)

type Credentials struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	EnablePassword string `json:"enablePassword,omitempty"`
}

// ConnectionRequest holds the fields common to every "connect to a device"
// request -- ExecuteRequest and FactsRequest both embed it. It's anonymous
// (embedded) in both so its JSON fields serialize at the same level as
// before this was factored out -- purely a Go-side dedup, the wire format
// for ExecuteRequest is unchanged from the original TypeScript
// implementation.
type ConnectionRequest struct {
	Host                 string      `json:"host"`
	Port                 int         `json:"port"`
	Protocol             string      `json:"protocol"`
	Vendor               string      `json:"vendor,omitempty"`
	Credentials          Credentials `json:"credentials"`
	DisablePagingCommand string      `json:"disablePagingCommand,omitempty"`
	ConnectTimeoutMs     *int        `json:"connectTimeoutMs,omitempty"`
	PerCommandTimeoutMs  *int        `json:"perCommandTimeoutMs,omitempty"`

	// SessionID, if set, means this request runs against an
	// already-open pooled session (see POST /v1/session/open) instead of
	// dialing a fresh connection -- every field above is ignored when
	// this is set (the session already has its own vendor/credentials
	// from whenever it was opened). See validateConnectionOrSession.
	SessionID string `json:"sessionId,omitempty"`
}

// validateConnection checks everything ExecuteRequest and FactsRequest
// have in common. Returns a human-readable message describing the first
// violation found, or "" if valid. Used unconditionally by
// SessionOpenRequest (opening a session always needs full connection
// details -- there's no session to reuse yet) and by every other
// request's validateConnectionOrSession below when SessionID is empty.
func (r *ConnectionRequest) validateConnection() string {
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
	if r.ConnectTimeoutMs != nil && (*r.ConnectTimeoutMs < 1 || *r.ConnectTimeoutMs > 60_000) {
		return "connectTimeoutMs must be between 1 and 60000"
	}
	if r.PerCommandTimeoutMs != nil && (*r.PerCommandTimeoutMs < 1 || *r.PerCommandTimeoutMs > 120_000) {
		return "perCommandTimeoutMs must be between 1 and 120000"
	}
	return ""
}

// validateConnectionOrSession is validateConnection's session-aware
// sibling, used by every per-operation request (Execute/Facts/
// Interfaces/LLDPNeighbors/Config/MergeConfig/MergeCmd -- NOT
// SessionOpenRequest, which always needs full connection details, or
// SessionCloseRequest, which only needs a SessionID and validates that
// itself). When SessionID is set, none of host/port/protocol/credentials
// matter -- the session already has an authenticated connection open, and
// its own stored vendor decides what's supported -- so this skips
// straight past validateConnection entirely rather than requiring fields
// the caller has no reason to send.
func (r *ConnectionRequest) validateConnectionOrSession() string {
	if r.SessionID != "" {
		return ""
	}
	return r.validateConnection()
}

func (r *ConnectionRequest) toConnectionParams() transport.ConnectionParams {
	connectTimeoutMs := transport.DefaultConnectTimeoutMs
	if r.ConnectTimeoutMs != nil {
		connectTimeoutMs = *r.ConnectTimeoutMs
	}
	perCommandTimeoutMs := transport.DefaultPerCommandTimeoutMs
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

// ExecuteRequest mirrors the original TypeScript ExecuteRequestSchema
// (zod) field-for-field. ConnectTimeoutMs/PerCommandTimeoutMs are pointers
// so Validate/the handler can distinguish "omitted" (use the default) from
// an explicit value.
type ExecuteRequest struct {
	ConnectionRequest
	Commands []string `json:"commands"`
}

// Validate returns a human-readable message describing the first
// violation found, or "" if the request is valid.
func (r *ExecuteRequest) Validate() string {
	if msg := r.validateConnectionOrSession(); msg != "" {
		return msg
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
	return ""
}

func (r *ExecuteRequest) ToConnectionParams() transport.ConnectionParams {
	return r.toConnectionParams()
}

// FactsRequest is ExecuteRequest's counterpart for the FactsProvider
// capability: same connection fields, no `commands` -- GetFacts() runs its
// own fixed set of commands internally (see each driver's implementation).
type FactsRequest struct {
	ConnectionRequest
}

func (r *FactsRequest) Validate() string {
	return r.validateConnectionOrSession()
}

func (r *FactsRequest) ToConnectionParams() transport.ConnectionParams {
	return r.toConnectionParams()
}

// ConfigRequest is ExecuteRequest's counterpart for the ConfigGetter
// capability: same connection fields, no `commands` -- GetConfig() runs
// its own fixed command internally (see each driver's implementation).
type ConfigRequest struct {
	ConnectionRequest
}

func (r *ConfigRequest) Validate() string {
	return r.validateConnectionOrSession()
}

func (r *ConfigRequest) ToConnectionParams() transport.ConnectionParams {
	return r.toConnectionParams()
}

// InterfacesRequest is ExecuteRequest's counterpart for the
// InterfaceProvider capability: same connection fields, no `commands` --
// GetInterfaces() runs its own fixed command internally (see each
// driver's implementation).
type InterfacesRequest struct {
	ConnectionRequest
}

func (r *InterfacesRequest) Validate() string {
	return r.validateConnectionOrSession()
}

func (r *InterfacesRequest) ToConnectionParams() transport.ConnectionParams {
	return r.toConnectionParams()
}

// LLDPNeighborsRequest is ExecuteRequest's counterpart for the
// LLDPNeighborsProvider capability: same connection fields, no
// `commands` -- GetLLDPNeighbors() runs its own fixed command internally
// (see each driver's implementation).
type LLDPNeighborsRequest struct {
	ConnectionRequest
}

func (r *LLDPNeighborsRequest) Validate() string {
	return r.validateConnectionOrSession()
}

func (r *LLDPNeighborsRequest) ToConnectionParams() transport.ConnectionParams {
	return r.toConnectionParams()
}

// batchOperationTypes: the only valid BatchOperation.Type values --
// deliberately read-only. Config *push* (-merge-config/-merge-cmd) is
// excluded on purpose: its dry-run/hash-freshness/interactive-confirm
// flow doesn't compose cleanly with "run N things in one connection and
// report what happened to each" -- what should the rest of a batch do if
// a merge step needs confirmation, or fails partway through applying?
// Keeping batch to reads sidesteps that entirely; merges stay their own
// dedicated, careful endpoints.
var batchOperationTypes = map[string]bool{
	"facts":          true,
	"interfaces":     true,
	"lldp-neighbors": true,
	"config":         true,
	"execute":        true,
}

// BatchOperation is one step of a BatchRequest -- Commands is only used
// (and required) when Type is "execute"; every other type ignores it.
type BatchOperation struct {
	Type     string   `json:"type"`
	Commands []string `json:"commands,omitempty"`
}

// BatchRequest is the single-connection, multiple-read-only-operations
// endpoint: gearctl opens ONE connection for the whole list of
// Operations (in order) and closes it once at the end, instead of a
// caller making N separate requests that each pay their own connection
// setup cost. This is the fix for the "NAPALM/Netmiko can amortize
// connection setup across multiple getters in one session, gearctl
// couldn't" finding -- captured with ZERO change to gearctl's stateless
// design (see README's Architecture section): the whole batch is still
// bounded by ONE request/response lifecycle, no session persists beyond
// it, no credentials are held anywhere past this one call returning.
//
// A vendor missing the capability a given operation needs doesn't fail
// the whole batch -- that operation's own result is "unsupported" (see
// BatchHandler), and every other operation in the list still runs.
type BatchRequest struct {
	ConnectionRequest
	Operations []BatchOperation `json:"operations"`
}

func (r *BatchRequest) Validate() string {
	if msg := r.validateConnection(); msg != "" {
		return msg
	}
	if len(r.Operations) == 0 {
		return "operations must have at least 1 entry"
	}
	if len(r.Operations) > 10 {
		return "operations must have at most 10 entries"
	}
	for i, op := range r.Operations {
		if !batchOperationTypes[op.Type] {
			return fmt.Sprintf(
				"operations[%d].type %q is not one of facts/interfaces/lldp-neighbors/config/execute "+
					"(config *push* isn't batchable -- use /v1/merge-config or /v1/merge-cmd directly)",
				i, op.Type,
			)
		}
		if op.Type == "execute" {
			if len(op.Commands) == 0 {
				return fmt.Sprintf("operations[%d]: commands must have at least 1 entry for type \"execute\"", i)
			}
			if len(op.Commands) > 50 {
				return fmt.Sprintf("operations[%d]: commands must have at most 50 entries", i)
			}
			for _, c := range op.Commands {
				if c == "" {
					return fmt.Sprintf("operations[%d]: commands entries must not be empty", i)
				}
			}
		}
	}
	return ""
}

func (r *BatchRequest) ToConnectionParams() transport.ConnectionParams {
	return r.toConnectionParams()
}

// MergeConfigRequest is ExecuteRequest's counterpart for the ConfigLoader
// capability (drivers.ConfigLoader.LoadConfig) -- named "merge", not
// "load" or "config-push", because the apply mechanism is a merge/overlay
// onto the existing config, never an atomic replace (see
// CiscoIOSDriver.LoadConfig's doc comment).
//
// DryRun is a required *bool (no implicit default -- omitting it is
// rejected by Validate, not silently treated as either true or false;
// this is a config-push feature, "which one did you mean" is not a safe
// question to guess the answer to). ExpectedConfigHash is required
// whenever DryRun is false: it must be the configHash a prior
// dryRun=true call returned, and LoadConfig refuses to apply if the
// device's live config no longer matches it (changed since that dry
// run).
type MergeConfigRequest struct {
	ConnectionRequest
	Candidate          string `json:"candidate"`
	DryRun             *bool  `json:"dryRun"`
	ExpectedConfigHash string `json:"expectedConfigHash,omitempty"`
}

func (r *MergeConfigRequest) Validate() string {
	if msg := r.validateConnectionOrSession(); msg != "" {
		return msg
	}
	if r.Candidate == "" {
		return "candidate is required"
	}
	if r.DryRun == nil {
		return "dryRun is required (true or false) -- there is no implicit default for a config-push feature"
	}
	if !*r.DryRun && r.ExpectedConfigHash == "" {
		return "expectedConfigHash is required when dryRun is false -- call with dryRun=true first and pass back the configHash it returns"
	}
	return ""
}

func (r *MergeConfigRequest) ToConnectionParams() transport.ConnectionParams {
	return r.toConnectionParams()
}

// MergeCmdRequest is MergeConfigRequest's delta-based sibling
// (drivers.ConfigLoader.LoadMergeCommands) -- same field shape as
// ExecuteRequest's Commands (a handful of lines to change) plus
// MergeConfigRequest's DryRun/ExpectedConfigHash contract. Reuses
// MergeConfigResponse for the response (same fields: diff, configHash,
// applied) -- see LoadMergeCommands's doc comment for why the diff shown
// is a projected preview, not a literal text diff of the input.
type MergeCmdRequest struct {
	ConnectionRequest
	Commands           []string `json:"commands"`
	DryRun             *bool    `json:"dryRun"`
	ExpectedConfigHash string   `json:"expectedConfigHash,omitempty"`
}

func (r *MergeCmdRequest) Validate() string {
	if msg := r.validateConnectionOrSession(); msg != "" {
		return msg
	}
	if len(r.Commands) == 0 {
		return "commands must have at least 1 entry"
	}
	for _, c := range r.Commands {
		if c == "" {
			return "commands entries must not be empty"
		}
	}
	if r.DryRun == nil {
		return "dryRun is required (true or false) -- there is no implicit default for a config-push feature"
	}
	if !*r.DryRun && r.ExpectedConfigHash == "" {
		return "expectedConfigHash is required when dryRun is false -- call with dryRun=true first and pass back the configHash it returns"
	}
	return ""
}

func (r *MergeCmdRequest) ToConnectionParams() transport.ConnectionParams {
	return r.toConnectionParams()
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

// FactsResponse is ExecuteResponse's counterpart for /v1/facts: Facts
// replaces Results. Nil Facts with a non-nil Error (e.g. type
// "unsupported") means the resolved driver doesn't implement
// FactsProvider for the given vendor.
type FactsResponse struct {
	Status          string                 `json:"status"`
	Facts           map[string]interface{} `json:"facts"`
	Error           *ExecuteError          `json:"error"`
	ConnectedAt     *string                `json:"connectedAt"`
	TotalDurationMs int64                  `json:"totalDurationMs"`
}

// ConfigResponse is ExecuteResponse's counterpart for /v1/config: Config
// replaces Results/Facts, holding the raw running-configuration text
// verbatim (no parsing). Nil Config with a non-nil Error (e.g. type
// "unsupported") means the resolved driver doesn't implement
// ConfigGetter for the given vendor.
type ConfigResponse struct {
	Status          string        `json:"status"`
	Config          *string       `json:"config"`
	Error           *ExecuteError `json:"error"`
	ConnectedAt     *string       `json:"connectedAt"`
	TotalDurationMs int64         `json:"totalDurationMs"`
}

// InterfacesResponse is ExecuteResponse's counterpart for
// /v1/interfaces: Interfaces (keyed by interface name) replaces Results.
// Nil Interfaces with a non-nil Error (e.g. type "unsupported") means the
// resolved driver doesn't implement InterfaceProvider for the given
// vendor.
type InterfacesResponse struct {
	Status          string                           `json:"status"`
	Interfaces      map[string]drivers.InterfaceInfo `json:"interfaces"`
	Error           *ExecuteError                    `json:"error"`
	ConnectedAt     *string                          `json:"connectedAt"`
	TotalDurationMs int64                            `json:"totalDurationMs"`
}

// LLDPNeighborsResponse is ExecuteResponse's counterpart for
// /v1/lldp-neighbors: Neighbors (keyed by local interface name) replaces
// Results. Nil Neighbors with a non-nil Error (e.g. type "unsupported")
// means the resolved driver doesn't implement LLDPNeighborsProvider for
// the given vendor -- but an empty, non-nil Neighbors map with no Error
// is a well-formed "LLDP is on, zero neighbors seen" answer; see
// drivers.CiscoIOSDriver.GetLLDPNeighbors's doc comment for how that's
// told apart from "LLDP isn't even enabled" (a distinct error instead).
type LLDPNeighborsResponse struct {
	Status          string                            `json:"status"`
	Neighbors       map[string][]drivers.LLDPNeighbor `json:"neighbors"`
	Error           *ExecuteError                     `json:"error"`
	ConnectedAt     *string                           `json:"connectedAt"`
	TotalDurationMs int64                             `json:"totalDurationMs"`
}

// BatchOperationResult is one entry of BatchResponse.Results, in the
// same order as the request's Operations. Only the field matching its
// own Type is ever populated (a "facts" op populates Facts, never
// Interfaces/Neighbors/Config/Results, etc.) -- Status/Error follow the
// same convention as every other handler's per-item result: "unsupported"
// if the vendor's driver doesn't implement the needed capability,
// "failed" for any other error, "success" otherwise. DurationMs is just
// this one operation's own time, not the whole batch's.
type BatchOperationResult struct {
	Type       string                            `json:"type"`
	Status     string                            `json:"status"`
	Facts      map[string]interface{}            `json:"facts,omitempty"`
	Interfaces map[string]drivers.InterfaceInfo  `json:"interfaces,omitempty"`
	Neighbors  map[string][]drivers.LLDPNeighbor `json:"neighbors,omitempty"`
	Config     *string                           `json:"config,omitempty"`
	Results    []transport.CommandResult         `json:"results,omitempty"`
	Error      *ExecuteError                     `json:"error,omitempty"`
	DurationMs int64                             `json:"durationMs"`
}

// BatchResponse is /v1/batch's response. Status is "success" only if
// EVERY operation succeeded, "partial" if some did and some didn't (same
// convention /v1/execute already uses for a mixed-result command list),
// "failed" if the connection itself never succeeded (in which case every
// operation's own Status is "failed" too, all with the same connection
// error). ConnectedAt is set once the connection succeeds, same meaning
// as every other endpoint's -- it says nothing about whether the
// operations that ran afterward succeeded. Error is request-level only
// (malformed JSON, validation failure, disallowed target) -- distinct
// from each BatchOperationResult's own Error, which is scoped to that
// one operation; Results is empty whenever Error is set, since the
// request never got far enough to even start any operation.
type BatchResponse struct {
	Status          string                 `json:"status"`
	Results         []BatchOperationResult `json:"results,omitempty"`
	Error           *ExecuteError          `json:"error,omitempty"`
	ConnectedAt     *string                `json:"connectedAt"`
	TotalDurationMs int64                  `json:"totalDurationMs"`
}

// MergeConfigResponse is /v1/merge-config's (and /v1/merge-cmd's, which
// reuses this same shape) response. Diff is populated on every
// successful call (dry run or apply, even a rejected or
// connection-dropped apply attempt) since it's computed before any write
// is attempted -- it's the record of what was being proposed. ConfigHash
// must be echoed back as ExpectedConfigHash on a follow-up dryRun=false
// call (see MergeConfigRequest's doc comment). Applied is only ever true
// on full, clean success.
//
// ReverseCommands is only ever populated by /v1/merge-cmd (LoadConfig has
// no equivalent -- see ConfigLoader's doc comment on why) -- the exact
// commands that would undo this merge, for a caller wanting to build
// "commit confirmed" semantics on top: apply, start your own timer, and
// if not explicitly confirmed in time, POST /v1/merge-cmd again with
// these as the new commands to revert. null on /v1/merge-config, and also
// whenever a delta had nothing to reconcile (e.g. every command was a
// "no ..." that matched nothing).
type MergeConfigResponse struct {
	Status          string        `json:"status"`
	Diff            *string       `json:"diff"`
	ConfigHash      *string       `json:"configHash"`
	ReverseCommands []string      `json:"reverseCommands"`
	Applied         bool          `json:"applied"`
	Error           *ExecuteError `json:"error"`
	ConnectedAt     *string       `json:"connectedAt"`
	TotalDurationMs int64         `json:"totalDurationMs"`
}

// SessionOpenRequest is POST /v1/session/open's request -- exactly
// ConnectionRequest's fields (a session always needs full connection
// details; there's nothing to reuse yet), embedded rather than
// duplicated. SessionID must NOT be set here -- see Validate.
type SessionOpenRequest struct {
	ConnectionRequest
}

func (r *SessionOpenRequest) Validate() string {
	if r.SessionID != "" {
		return "sessionId must not be set when opening a new session"
	}
	return r.validateConnection()
}

func (r *SessionOpenRequest) ToConnectionParams() transport.ConnectionParams {
	return r.toConnectionParams()
}

// SessionOpenResponse is POST /v1/session/open's response. SessionID is
// the opaque token to pass as every subsequent request's `sessionId`
// field instead of host/credentials -- see internal/sessionpool's package
// doc comment for why this is a fresh random value, never anything
// derived from the credentials that opened it. ConnectedAt is when the
// underlying connection was established (once, here) -- every later
// operation against this session echoes the SAME ConnectedAt back,
// since it describes the connection, not any one operation run over it.
type SessionOpenResponse struct {
	Status          string        `json:"status"`
	SessionID       string        `json:"sessionId,omitempty"`
	Vendor          string        `json:"vendor,omitempty"`
	Error           *ExecuteError `json:"error"`
	ConnectedAt     *string       `json:"connectedAt"`
	TotalDurationMs int64         `json:"totalDurationMs"`
}

// SessionCloseRequest is POST /v1/session/close's request -- just the
// token to close. Closing an already-closed or unknown session is a
// clearly-reported error (session_not_found), not silently treated as
// success -- a caller relying on close to free a resource deserves to
// know if there was nothing to free.
type SessionCloseRequest struct {
	SessionID string `json:"sessionId"`
}

func (r *SessionCloseRequest) Validate() string {
	if r.SessionID == "" {
		return "sessionId is required"
	}
	return ""
}

type SessionCloseResponse struct {
	Status          string        `json:"status"`
	Error           *ExecuteError `json:"error"`
	TotalDurationMs int64         `json:"totalDurationMs"`
}
