package tools

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// normalizePublicHTTPURL validates that raw is a public http(s) URL and
// returns it in canonical form. Anything else is refused before dialing:
// a model-supplied URL is untrusted input, and "fetch this URL" must not
// become "probe my metadata service" or "hit this intranet host".
//
// Rejected: backslashes, control characters and spaces, non-http(s)
// schemes, userinfo (`user@host`), %-encoded hosts, localhost and
// loopback in any spelling, single-label hostnames, and non-global IPs —
// including legacy inet_aton forms (`2130706433`, `0x7f.1`, `127.1`)
// that parse to 127.0.0.1 but dodge string-prefix checks.
//
// check_url allows loopback (probing a just-started dev server on
// localhost is its entire job) via normalizeLoopbackURL; everything else
// uses this strict form.
func normalizePublicHTTPURL(raw string) (string, error) {
	return normalizeURL(raw, false)
}

// normalizeLoopbackURL is the check_url form: same rules, except
// localhost and loopback literals are permitted. The result it returns
// is still only a status plus a 512-byte prefix, never file contents, so
// a permitted loopback probe cannot exfiltrate anything beyond "is it up".
func normalizeLoopbackURL(raw string) (string, error) {
	return normalizeURL(raw, true)
}

func normalizeURL(raw string, allowLoopback bool) (string, error) {
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("bad url %q: backslashes are not valid in URLs", raw)
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7F || r == ' ' {
			return "", fmt.Errorf("bad url %q: control characters and spaces are not valid", raw)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("bad url %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("bad url %q: only http and https are fetched", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("bad url %q: userinfo is never sent", raw)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("bad url %q: no host", raw)
	}
	if strings.Contains(host, "%") {
		return "", fmt.Errorf("bad url %q: encoded hosts are refused", raw)
	}
	lower := strings.ToLower(host)
	isLocalhost := lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".localhost.")
	if isLocalhost && !allowLoopback {
		return "", fmt.Errorf("bad url %q: %s is not public", raw, host)
	}
	if lower == "metadata.google.internal" || strings.HasSuffix(lower, ".internal") ||
		strings.HasSuffix(lower, ".lan") || strings.HasSuffix(lower, ".local") {
		return "", fmt.Errorf("bad url %q: %s is not public", raw, host)
	}
	if ip := parseIPLoose(host); ip != nil {
		if ip.IsLoopback() && allowLoopback {
			// check_url probing its own dev server — permitted, see above.
		} else if !ip.IsGlobalUnicast() || !isPublicIP(ip) {
			return "", fmt.Errorf("bad url %q: %s is not a public address", raw, host)
		}
	} else {
		// Not an IP literal: require a dotted name. Single-label names
		// (`intranet`, `printer`) resolve via local search domains.
		if !strings.Contains(host, ".") && !(allowLoopback && isLocalhost) {
			return "", fmt.Errorf("bad url %q: single-label hostnames are refused", raw)
		}
	}
	u.RawQuery = u.Query().Encode()
	return u.String(), nil
}

// parseIPLoose parses dotted IP literals including the legacy inet_aton
// forms getaddrinfo still accepts: fewer than four parts, leading zeros,
// and hex/octal components. Standard net.ParseIP misses most of these,
// which is exactly why blocklists written against it can be bypassed.
func parseIPLoose(host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	parts := strings.Split(host, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return nil
	}
	var nums []uint64
	for _, p := range parts {
		if p == "" {
			return nil
		}
		var n uint64
		var err error
		switch {
		case strings.HasPrefix(p, "0x") || strings.HasPrefix(p, "0X"):
			_, err = fmt.Sscanf(p[2:], "%x", &n)
		case len(p) > 1 && p[0] == '0':
			_, err = fmt.Sscanf(p, "%o", &n)
		default:
			_, err = fmt.Sscanf(p, "%d", &n)
		}
		if err != nil || n > 255 {
			// The final part of a short form may hold up to 3 bytes
			// (`127.1`); validate below after combining.
			if err != nil {
				return nil
			}
		}
		nums = append(nums, n)
	}
	var addr uint32
	switch len(nums) {
	case 1:
		if nums[0] > 0xFFFFFFFF {
			return nil
		}
		addr = uint32(nums[0])
	case 2:
		if nums[0] > 255 || nums[1] > 0xFFFFFF {
			return nil
		}
		addr = uint32(nums[0])<<24 | uint32(nums[1])
	case 3:
		if nums[0] > 255 || nums[1] > 255 || nums[2] > 0xFFFF {
			return nil
		}
		addr = uint32(nums[0])<<24 | uint32(nums[1])<<16 | uint32(nums[2])
	case 4:
		for _, n := range nums {
			if n > 255 {
				return nil
			}
		}
		addr = uint32(nums[0])<<24 | uint32(nums[1])<<16 | uint32(nums[2])<<8 | uint32(nums[3])
	}
	return net.IPv4(byte(addr>>24), byte(addr>>16), byte(addr>>8), byte(addr))
}

// isPublicIP reports whether ip is routable on the public internet:
// not loopback, private, link-local, multicast, unspecified, or reserved.
func isPublicIP(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified() &&
		!isReserved(ip)
}

// isReserved covers ranges net.IP methods miss: benchmarking, TEST-NET,
// 6to4/teredo relays, and IPv4-mapped documentation space.
func isReserved(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 2: // TEST-NET-1
			return true
		case ip4[0] == 198 && ip4[1] == 51 && ip4[2] == 100: // TEST-NET-2
			return true
		case ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113: // TEST-NET-3
			return true
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // benchmarking
			return true
		case ip4[0] == 192 && ip4[1] == 88 && ip4[2] == 99: // 6to4 relay
			return true
		}
		return false
	}
	// IPv6 documentation prefix.
	return strings.HasPrefix(strings.ToLower(ip.String()), "2001:db8")
}
