package ssrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrBlockedAddress = errors.New("endpoint resolves to a non-public network address")

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Policy struct {
	resolver     Resolver
	dialer       net.Dialer
	allowPrivate bool
}

func NewPolicy(allowPrivate bool) *Policy {
	return &Policy{
		resolver:     net.DefaultResolver,
		dialer:       net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
		allowPrivate: allowPrivate,
	}
}

func (p *Policy) ValidateURL(ctx context.Context, raw string) error {
	parsed, err := parseEndpointURL(raw)
	if err != nil {
		return err
	}
	_, err = p.resolveAllowed(ctx, parsed.Hostname())
	return err
}

func (p *Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse outbound address: %w", err)
	}
	addresses, err := p.resolveAllowed(ctx, host)
	if err != nil {
		return nil, err
	}
	var dialErrors []error
	for _, ip := range addresses {
		conn, err := p.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErrors = append(dialErrors, err)
	}
	return nil, fmt.Errorf("dial endpoint: %w", errors.Join(dialErrors...))
}

func parseEndpointURL(raw string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Hostname() == "" || !parsed.IsAbs() {
		return nil, errors.New("url must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("url scheme must be http or https")
	}
	if parsed.User != nil {
		return nil, errors.New("url must not contain credentials")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("url must not contain a fragment")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, errors.New("url contains an invalid port")
		}
	}
	return parsed, nil
}

func (p *Policy) resolveAllowed(ctx context.Context, host string) ([]netip.Addr, error) {
	normalized := strings.TrimSuffix(strings.ToLower(host), ".")
	if !p.allowPrivate &&
		(normalized == "localhost" || strings.HasSuffix(normalized, ".localhost")) {
		return nil, ErrBlockedAddress
	}

	var addresses []netip.Addr
	if literal, err := netip.ParseAddr(normalized); err == nil {
		addresses = []netip.Addr{literal}
	} else {
		resolved, err := p.resolver.LookupNetIP(ctx, "ip", normalized)
		if err != nil {
			return nil, fmt.Errorf("resolve endpoint hostname: %w", err)
		}
		addresses = resolved
	}
	if len(addresses) == 0 {
		return nil, errors.New("endpoint hostname has no IP addresses")
	}

	allowed := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if address.Zone() != "" {
			return nil, ErrBlockedAddress
		}
		if !p.allowPrivate && blocked(address) {
			return nil, ErrBlockedAddress
		}
		if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() {
			return nil, ErrBlockedAddress
		}
		allowed = append(allowed, address)
	}
	return allowed, nil
}

func blocked(address netip.Addr) bool {
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return !address.IsGlobalUnicast()
}

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
	"2001:db8::/32",
)

func mustPrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}
