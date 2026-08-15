package drivers

// RawDriver is the generic, vendor-agnostic driver: opens an SSH or Telnet
// session and runs whatever commands the caller supplied, with zero
// parsing/vendor knowledge. This satisfies the literal requirement ("SSH or
// Telnet into a device and run commands") for any device, regardless of
// vendor -- it's also Resolve's fallback for any vendor without a
// dedicated driver (see ciscoiosdriver.go for the first one).
//
// All session-lifecycle behavior (Connect/RunCommands/Disconnect) comes
// from the embedded BaseDriver; RawDriver itself only declares its
// vendor identity.
type RawDriver struct {
	BaseDriver
}

func NewRawDriver() *RawDriver {
	return &RawDriver{}
}

func (d *RawDriver) Vendor() string {
	return "*"
}
