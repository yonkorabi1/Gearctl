// Package security provides target-host validation, INVERTED from the SSRF
// guard pattern used elsewhere in this platform (platform/api's
// device-command.processor.ts isAllowedUrl(), which blocks ALL
// private/RFC1918 ranges since it validates operator-supplied download URLs
// that should point somewhere public).
//
// Here, the expected/normal case is the OPPOSITE: OLT management IPs
// legitimately live on private management VLANs (RFC1918). What this guard
// actually needs to block is the gateway process connecting to itself or to
// cloud-metadata endpoints -- loopback and link-local only, not all private
// ranges. Also: in this platform, platform-api resolves the target host
// itself from the `olts` table by ID (never from unvalidated client input),
// so this is a defense-in-depth backstop, not the only guard.
package security

import (
	"net"
	"strconv"
	"strings"
)

var blockedHostnames = map[string]bool{
	"localhost": true,
}

var metadataIPs = map[string]bool{
	"169.254.169.254": true,
}

// IsAllowedOltTarget mirrors the TypeScript implementation's
// isAllowedOltTarget() exactly, including the decimal/octal/hex IP-literal
// detection (transport-agnostic input sanitization, reused in spirit from
// the platform's isAllowedUrl()).
func IsAllowedOltTarget(host string) bool {
	hostname := strings.ToLower(strings.TrimSpace(host))

	if blockedHostnames[hostname] {
		return false
	}
	if hostname == "127.0.0.1" || hostname == "::1" || hostname == "[::1]" || hostname == "0.0.0.0" {
		return false
	}
	if metadataIPs[hostname] {
		return false
	}

	// Decimal IP notation (e.g. 2130706433 === 127.0.0.1)
	if isAllDigits(hostname) {
		decimal, err := strconv.ParseInt(hostname, 10, 64)
		if err == nil && decimal >= 0 && decimal <= 0xffffffff {
			return !isBlockedIPv4FromInt(uint32(decimal))
		}
		return false
	}

	// Octal IP notation (e.g. 0177.0.0.1 === 127.0.0.1)
	octets := strings.Split(hostname, ".")
	if len(octets) == 4 {
		for _, o := range octets {
			if len(o) > 1 && o[0] == '0' && isAllDigits(o) {
				return false
			}
		}
	}

	// Hex IP notation -- suspicious enough on its own to reject outright,
	// matching the existing isAllowedUrl() precedent.
	if strings.HasPrefix(hostname, "0x") && isAllHex(hostname[2:]) && len(hostname) > 2 {
		return false
	}

	if ip := net.ParseIP(hostname); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return !isBlockedIPv4Octets(ip4[0], ip4[1])
		}
		// IPv6 loopback/link-local
		raw := strings.Trim(hostname, "[]")
		if raw == "::1" || raw == "::" || strings.HasPrefix(raw, "fe80") {
			return false
		}
		return true
	}

	// A bare hostname (not an IP literal) -- allowed. platform-api always
	// passes a management_ip from the `olts` table, so this path is only
	// reachable via direct calls to this service, which is internal-only.
	return true
}

func isBlockedIPv4Octets(a, b byte) bool {
	if a == 127 {
		return true // loopback
	}
	if a == 169 && b == 254 {
		return true // link-local / cloud metadata
	}
	if a == 0 {
		return true // "this network"
	}
	return false
}

func isBlockedIPv4FromInt(ip uint32) bool {
	a := byte(ip >> 24)
	b := byte(ip >> 16)
	return isBlockedIPv4Octets(a, b)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isAllHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
