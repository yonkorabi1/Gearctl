package drivers

import "testing"

func TestResolveKnownVendorsGetDedicatedDrivers(t *testing.T) {
	for _, vendor := range []string{"cisco", "Cisco", "  CISCO  "} {
		d := Resolve(vendor)
		if _, ok := d.(*CiscoIOSDriver); !ok {
			t.Errorf("Resolve(%q) = %T, want *CiscoIOSDriver", vendor, d)
		}
		if got := d.Vendor(); got != "cisco" {
			t.Errorf("Resolve(%q).Vendor() = %q, want \"cisco\"", vendor, got)
		}
	}
}

func TestResolveFallsBackToRawDriverForUnknownVendors(t *testing.T) {
	for _, vendor := range []string{"", "huawei", "zte", "unknown-vendor"} {
		d := Resolve(vendor)
		if _, ok := d.(*RawDriver); !ok {
			t.Errorf("Resolve(%q) = %T, want *RawDriver", vendor, d)
		}
		if got := d.Vendor(); got != "*" {
			t.Errorf("Resolve(%q).Vendor() = %q, want \"*\"", vendor, got)
		}
	}
}
