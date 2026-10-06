package tools

import (
	"context"
	"net"
	"testing"
)

func TestDialRejectsPrivateDNSBeforeConnecting(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "198.18.0.1", "240.0.0.1", "::1"} {
		called := false
		_, err := dialPublic(context.Background(), "tcp", "example.com:80", false, func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil }, func(context.Context, string, string) (net.Conn, error) { called = true; return nil, nil })
		if err == nil || called {
			t.Fatalf("private destination %s dialed", ip)
		}
	}
}
func TestDialUsesValidatedLiteral(t *testing.T) {
	conn, err := dialPublic(context.Background(), "tcp", "example.com:80", false, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}, func(_ context.Context, _ string, address string) (net.Conn, error) {
		if address != "8.8.8.8:80" {
			t.Fatalf("re-resolved address: %s", address)
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}
