package tools

import (
	"strings"
	"testing"
)

func TestNormalizePublicHTTPURL_AcceptsPublic(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/page?q=1",
		"http://example.com:8080/a/b",
		"https://sub.domain.co.uk/",
		"https://93.184.216.34/",
	} {
		got, err := normalizePublicHTTPURL(raw)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if !strings.HasPrefix(got, "http") {
			t.Errorf("%s -> %q", raw, got)
		}
	}
}

func TestNormalizePublicHTTPURL_Rejects(t *testing.T) {
	cases := map[string]string{
		// Wrong scheme / shape.
		"ftp://example.com/x":   "scheme",
		"file:///etc/passwd":    "scheme",
		"example.com/no-scheme": "scheme",
		"":                      "host",
		// Smuggled authority.
		"https://user@example.com/":     "userinfo",
		"https://user:pass@example.com/": "userinfo",
		`https:\\example.com\`:          "backslash",
		"https://exam ple.com/":          "spaces",
		// Encoded / non-public hosts.
		"https://%65xample.com/":              "encoded",
		"https://localhost:3000/":             "localhost",
		"https://LOCALHOST/":                  "localhost",
		"https://foo.localhost/":              "localhost",
		"http://metadata.google.internal/":   "internal",
		"https://db.internal/":                "internal",
		"https://printer.lan/":                "lan",
		"https://nas.local/":                  "local",
		"https://intranet/":                   "single-label",
		// Non-global IPs in plain and legacy inet_aton spellings.
		"http://127.0.0.1/":      "loopback",
		"http://127.1/":          "loopback short form",
		"http://2130706433/":     "loopback decimal",
		"http://0x7f.0.0.1/":     "loopback hex",
		"http://0177.0.0.1/":     "loopback octal",
		"http://10.0.0.5/":       "private",
		"http://192.168.1.1/":    "private",
		"http://172.16.0.1/":     "private",
		"http://169.254.10.20/":  "link-local",
		"http://0.0.0.0/":        "unspecified",
		"http://224.0.0.1/":      "multicast",
		"http://192.0.2.1/":      "TEST-NET",
		"http://[::1]/":          "ipv6 loopback",
		"http://[fe80::1]/":      "ipv6 link-local",
		"http://[2001:db8::1]/":  "ipv6 documentation",
	}
	for raw, why := range cases {
		if got, err := normalizePublicHTTPURL(raw); err == nil {
			t.Errorf("%s (%s): accepted as %q", raw, why, got)
		}
	}
}

func TestNormalizeLoopbackURL_AllowsLocalProbes(t *testing.T) {
	// check_url exists to probe just-started dev servers, so loopback in
	// any spelling passes — everything else follows the strict rules.
	for _, raw := range []string{
		"http://localhost:5173/",
		"http://127.0.0.1:3000/",
		"http://127.1/",
		"http://[::1]:8080/",
	} {
		if _, err := normalizeLoopbackURL(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://10.0.0.5/",
		"http://192.168.1.1/",
		"http://metadata.google.internal/",
		"https://user@example.com/",
		"ftp://localhost/x",
	} {
		if got, err := normalizeLoopbackURL(raw); err == nil {
			t.Errorf("%s: accepted as %q", raw, got)
		}
	}
}

func TestParseIPLoose_LegacyForms(t *testing.T) {
	for raw, want := range map[string]string{
		"127.1":      "127.0.0.1",
		"2130706433": "127.0.0.1",
		"0x7f.0.0.1": "127.0.0.1",
		"0177.0.0.1": "127.0.0.1",
		"10.1":       "10.0.0.1",
	} {
		ip := parseIPLoose(raw)
		if ip == nil || ip.String() != want {
			t.Errorf("%s -> %v, want %s", raw, ip, want)
		}
	}
	if parseIPLoose("example.com") != nil {
		t.Error("hostname must not parse as IP")
	}
	if parseIPLoose("999.1.1.1") != nil {
		t.Error("out-of-range part must not parse")
	}
}
