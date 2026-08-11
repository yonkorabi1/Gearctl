// Package olterr defines the error-type vocabulary shared across the
// gateway: transport code raises these, and the HTTP layer maps them
// straight onto the ExecuteResponse.error.type field the same way the
// original TypeScript implementation's OltCliError did.
package olterr

type Type string

const (
	TypeAuth       Type = "auth"
	TypeTimeout    Type = "timeout"
	TypeConnection Type = "connection"
	TypeProtocol   Type = "protocol"
	TypeValidation Type = "validation"
)

// OltCliError is the gateway's error type -- callers type-assert with
// errors.As to recover the classification (Type) instead of just a bare
// message, the same way the TS version's OltCliError.type worked.
type OltCliError struct {
	ErrType Type
	Msg     string
}

func New(t Type, msg string) *OltCliError {
	return &OltCliError{ErrType: t, Msg: msg}
}

func (e *OltCliError) Error() string {
	return e.Msg
}
