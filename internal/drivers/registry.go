package drivers

import "strings"

// Resolve returns a driver for a given vendor hint (the free-text value
// carried through from the `olts`/device table -- see
// transport.ConnectionParams.Vendor). Recognized vendors get a dedicated
// driver; anything else (empty, unknown, or a vendor without a driver yet)
// falls back to RawDriver, so unrecognized vendors keep working exactly as
// before -- adding a driver here never breaks an existing one.
func Resolve(vendor string) CliDriver {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "cisco":
		return NewCiscoIOSDriver()
	case "juniper":
		return NewJuniperDriver()
	default:
		return NewRawDriver()
	}
}
