package drivers

// Resolve returns a driver for a given vendor hint. Phase 1 always returns
// RawDriver regardless of `vendor` -- the field is accepted in the request
// purely for audit-trail/forward-compat. Phase 2 adds vendor-specific
// drivers here (e.g. `case "huawei": return NewHuaweiOltDriver()`) without
// touching the HTTP route, transport layer, or the RawDriver fallback for
// unrecognized/unknown vendors.
func Resolve(_ string) CliDriver {
	return NewRawDriver()
}
