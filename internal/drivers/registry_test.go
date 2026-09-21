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

func TestResolveKnownExtremeAliasesGetExtremeDriver(t *testing.T) {
	for _, vendor := range []string{"extreme", "Extreme", "exos", "EXOS", "  extreme  "} {
		d := Resolve(vendor)
		if _, ok := d.(*ExtremeDriver); !ok {
			t.Errorf("Resolve(%q) = %T, want *ExtremeDriver", vendor, d)
		}
		if got := d.Vendor(); got != "extreme" {
			t.Errorf("Resolve(%q).Vendor() = %q, want \"extreme\"", vendor, got)
		}
	}
}

func TestResolveKnownMikroTikAliasesGetMikroTikDriver(t *testing.T) {
	for _, vendor := range []string{"mikrotik", "MikroTik", "routeros", "RouterOS", "  mikrotik  "} {
		d := Resolve(vendor)
		if _, ok := d.(*MikroTikDriver); !ok {
			t.Errorf("Resolve(%q) = %T, want *MikroTikDriver", vendor, d)
		}
		if got := d.Vendor(); got != "mikrotik" {
			t.Errorf("Resolve(%q).Vendor() = %q, want \"mikrotik\"", vendor, got)
		}
	}
}

func TestResolveKnownHuaweiAliasesGetHuaweiDriver(t *testing.T) {
	for _, vendor := range []string{"huawei", "Huawei", "  HUAWEI  "} {
		d := Resolve(vendor)
		if _, ok := d.(*HuaweiDriver); !ok {
			t.Errorf("Resolve(%q) = %T, want *HuaweiDriver", vendor, d)
		}
		if got := d.Vendor(); got != "huawei" {
			t.Errorf("Resolve(%q).Vendor() = %q, want \"huawei\"", vendor, got)
		}
	}
}

func TestResolveFallsBackToRawDriverForUnknownVendors(t *testing.T) {
	for _, vendor := range []string{"", "zte", "unknown-vendor"} {
		d := Resolve(vendor)
		if _, ok := d.(*RawDriver); !ok {
			t.Errorf("Resolve(%q) = %T, want *RawDriver", vendor, d)
		}
		if got := d.Vendor(); got != "*" {
			t.Errorf("Resolve(%q).Vendor() = %q, want \"*\"", vendor, got)
		}
	}
}
