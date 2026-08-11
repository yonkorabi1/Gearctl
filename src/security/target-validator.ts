// ============================================================================
// Target-host validation, INVERTED from the SSRF guard pattern used
// elsewhere in this platform (platform/api's device-command.processor.ts
// isAllowedUrl(), which blocks ALL private/RFC1918 ranges since it
// validates operator-supplied download URLs that should point somewhere
// public).
//
// Here, the expected/normal case is the OPPOSITE: OLT management IPs
// legitimately live on private management VLANs (RFC1918). What this
// guard actually needs to block is the gateway process connecting to
// itself or to cloud-metadata endpoints -- loopback and link-local only,
// not all private ranges. Also: in this platform, platform-api resolves
// the target host itself from the `olts` table by ID (never from
// unvalidated client input) -- see the plan's "B.4" section -- so this is
// a defense-in-depth backstop, not the only guard.
//
// Decimal/octal/hex IP-literal detection is transport-agnostic input
// sanitization and is reused verbatim in spirit from isAllowedUrl().
// ============================================================================

const BLOCKED_HOSTNAMES = new Set(['localhost']);
const METADATA_IPS = new Set(['169.254.169.254']);

export function isAllowedOltTarget(host: string): boolean {
  const hostname = host.trim().toLowerCase();

  if (BLOCKED_HOSTNAMES.has(hostname)) return false;
  if (hostname === '127.0.0.1' || hostname === '::1' || hostname === '[::1]' || hostname === '0.0.0.0') {
    return false;
  }
  if (METADATA_IPS.has(hostname)) return false;

  // Decimal IP notation (e.g. 2130706433 === 127.0.0.1)
  if (/^\d+$/.test(hostname)) {
    const decimal = Number(hostname);
    if (Number.isFinite(decimal) && decimal >= 0 && decimal <= 0xffffffff) {
      return !isBlockedIpv4FromInt(decimal);
    }
    return false;
  }

  // Octal IP notation (e.g. 0177.0.0.1 === 127.0.0.1)
  const octets = hostname.split('.');
  if (octets.length === 4 && octets.some((o) => o.length > 1 && o.startsWith('0') && /^\d+$/.test(o))) {
    return false;
  }

  // Hex IP notation -- suspicious enough on its own to reject outright,
  // matching the existing isAllowedUrl() precedent.
  if (/^0x[0-9a-f]+$/.test(hostname)) {
    return false;
  }

  const ipv4Match = hostname.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
  if (ipv4Match) {
    const [, a, b] = ipv4Match.slice(1).map(Number);
    return !isBlockedIpv4Octets(a, b);
  }

  // IPv6 loopback/link-local
  const raw = hostname.replace(/^\[|\]$/g, '');
  if (raw === '::1' || raw === '::' || raw.startsWith('fe80')) {
    return false;
  }

  // A bare hostname (not an IP literal) -- allowed. platform-api always
  // passes a management_ip from the `olts` table, so this path is only
  // reachable via direct calls to this service, which is internal-only.
  return true;
}

function isBlockedIpv4Octets(a: number, b: number): boolean {
  if (a === 127) return true; // loopback
  if (a === 169 && b === 254) return true; // link-local / cloud metadata
  if (a === 0) return true; // "this network"
  return false;
}

function isBlockedIpv4FromInt(ip: number): boolean {
  const a = (ip >>> 24) & 0xff;
  const b = (ip >>> 16) & 0xff;
  return isBlockedIpv4Octets(a, b);
}
