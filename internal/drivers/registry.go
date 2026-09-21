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
	case "extreme", "exos":
		// "exos" is an explicit disambiguator: ExtremeDriver targets
		// ExtremeXOS specifically, NOT Extreme's other CLI family
		// (VOSS/Fabric Engine, modal like Cisco IOS) -- see
		// extremedriver.go's doc comment. No dedicated VOSS driver exists
		// yet, so "extreme" also resolves here rather than to RawDriver.
		return NewExtremeDriver()
	case "mikrotik", "routeros":
		// "routeros" is an explicit alias: RouterOS is the CLI, MikroTik
		// the vendor -- both hints are common in the wild, and there's
		// only ever one driver either could mean.
		return NewMikroTikDriver()
	case "huawei":
		return NewHuaweiDriver()
	default:
		return NewRawDriver()
	}
}
