package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// networkClient enforces the destination at redirect and dial time. Proxy
// environment variables are deliberately ignored: a proxy would bypass DNS
// validation of the actual destination.
func networkClient(allowLoopback bool, override http.RoundTripper) *http.Client {
	tr := publicTransport
	if allowLoopback {
		tr = loopbackTransport
	}
	var transport http.RoundTripper = tr
	if override != nil {
		transport = override
	}
	return &http.Client{Transport: &validatedTransport{transport, allowLoopback}, Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		_, err := normalizeURL(req.URL.String(), allowLoopback)
		return err
	}}
}

var publicTransport = newNetworkTransport(false)
var loopbackTransport = newNetworkTransport(true)

// NetworkClient allows public addresses and optionally loopback dev servers.
func NetworkClient(allowLoopback bool) *http.Client { return networkClient(allowLoopback, nil) }

func newNetworkTransport(allowLoopback bool) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialPublic(ctx, network, address, allowLoopback, net.DefaultResolver.LookupIPAddr, (&net.Dialer{Timeout: 10 * time.Second}).DialContext)
	}
	return tr
}

func dialPublic(ctx context.Context, network, address string, allowLoopback bool, lookup func(context.Context, string) ([]net.IPAddr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	for _, ip := range ips {
		if !isPublicIP(ip.IP) && !(allowLoopback && ip.IP.IsLoopback()) {
			return nil, fmt.Errorf("refused non-public address for %s", host)
		}
	}
	// Dial the validated literal, never resolve the hostname a second time.
	for _, ip := range ips {
		conn, e := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if e == nil {
			return conn, nil
		}
		err = e
	}
	return nil, err
}

type validatedTransport struct {
	inner         http.RoundTripper
	allowLoopback bool
}

func (t *validatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, err := normalizeURL(req.URL.String(), t.allowLoopback); err != nil {
		return nil, err
	}
	return t.inner.RoundTrip(req)
}
