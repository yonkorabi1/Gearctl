package security

import "testing"

func TestIsAllowedOltTarget(t *testing.T) {
	cases := []struct {
		name string
		host string
		want bool
	}{
		{"private management IP is allowed", "192.168.17.40", true},
		{"another RFC1918 range is allowed", "10.0.0.1", true},
		{"172.16 range is allowed", "172.16.5.5", true},
		{"loopback IPv4 is blocked", "127.0.0.1", false},
		{"loopback hostname is blocked", "localhost", false},
		{"loopback hostname is case-insensitive", "LocalHost", false},
		{"IPv6 loopback is blocked", "::1", false},
		{"bracketed IPv6 loopback is blocked", "[::1]", false},
		{"link-local/metadata is blocked", "169.254.169.254", false},
		{"link-local range is blocked", "169.254.1.1", false},
		{"this-network is blocked", "0.0.0.0", false},
		{"decimal IP encoding of loopback is blocked", "2130706433", false},
		{"decimal IP encoding of a private IP is allowed", "3232235777", true}, // 192.168.1.1
		{"octal-looking octet is blocked outright", "0177.0.0.1", false},
		{"hex-prefixed literal is blocked outright", "0x7f000001", false},
		{"bare hostname is allowed", "olt-site-a.internal", true},
		{"public IP is allowed (not this guard's job)", "8.8.8.8", true},
		{"IPv6 link-local is blocked", "fe80::1", false},
		{"ordinary IPv6 is allowed", "2001:db8::1", true},
		{"empty string is not an IP/hostname match, allowed through", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAllowedOltTarget(tc.host)
			if got != tc.want {
				t.Errorf("IsAllowedOltTarget(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}
