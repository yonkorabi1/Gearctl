package transport

import "testing"

func TestIsEnableStyleVendor(t *testing.T) {
	cases := []struct {
		vendor string
		want   bool
	}{
		{"cisco", true},
		{"Cisco", true},
		{"  ARISTA  ", true},
		{"huawei", false},
		{"juniper", false},
		{"junos", false},
		{"HSGQ", false}, // OLT-WRK-LTP-01's vendor tag -- not confirmed to need enable
		{"", false},
		{"unknown-vendor", false},
	}
	for _, tc := range cases {
		t.Run(tc.vendor, func(t *testing.T) {
			if got := isEnableStyleVendor(tc.vendor); got != tc.want {
				t.Errorf("isEnableStyleVendor(%q) = %v, want %v", tc.vendor, got, tc.want)
			}
		})
	}
}
