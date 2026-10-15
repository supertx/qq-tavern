// Package netguard fetches untrusted public URLs without reaching local services.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func PublicIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !blocked(ip)
}
func blocked(ip net.IP) bool {
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96"} {
		_, n, _ := net.ParseCIDR(cidr)
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
func Validate(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("only public HTTP(S) URLs are allowed")
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return errors.New("only public HTTP ports are allowed")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !PublicIP(ip) {
		return errors.New("private or reserved address is blocked")
	}
	if strings.EqualFold(u.Hostname(), "localhost") || strings.HasSuffix(strings.ToLower(u.Hostname()), ".localhost") {
		return errors.New("localhost is blocked")
	}
	return nil
}
func NewClient() *http.Client {
	tr := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second, MaxIdleConns: 16, IdleConnTimeout: time.Minute}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, errors.New("DNS lookup failed")
		}
		if len(ips) == 0 {
			return nil, errors.New("DNS returned no address")
		}
		for _, a := range ips {
			if !PublicIP(a.IP) {
				return nil, errors.New("private or reserved address is blocked")
			}
		}
		var last error
		for _, a := range ips {
			c, e := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
			if e == nil {
				return c, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return Validate(r.URL.String())
	}}
}
func Fetch(ctx context.Context, client *http.Client, raw string, max int64) ([]byte, string, error) {
	if e := Validate(raw); e != nil {
		return nil, "", e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, "", e
	}
	req.Header.Set("User-Agent", "qq-tavern/0.1 (+https://github.com/supertx/qq-tavern)")
	resp, e := client.Do(req)
	if e != nil {
		return nil, "", errors.New("web request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("web returned HTTP %d; do not retry this URL in this turn", resp.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if e != nil {
		return nil, "", e
	}
	if int64(len(data)) > max {
		return nil, "", errors.New("download exceeds size limit")
	}
	return data, resp.Header.Get("Content-Type"), nil
}
