package clientip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func ParseTrustedProxies(values []string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			ip, ipErr := netip.ParseAddr(value)
			if ipErr != nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", value)
			}
			ip = ip.Unmap()
			prefix = netip.PrefixFrom(ip, ip.BitLen())
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

func trustedProxy(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

func RequestSourceIP(request *http.Request, prefixes []netip.Prefix) string {
	peer := request.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	ip, err := netip.ParseAddr(peer)
	if err != nil || !trustedProxy(prefixes, ip) {
		return peer
	}
	// The closest trusted proxy appends the sender it actually observed.
	// Stop at the first untrusted hop; leftmost client-supplied values cannot
	// override that address. Malformed chains fall back to the TCP peer.
	if values := request.Header.Values("X-Forwarded-For"); len(values) > 0 {
		chain := strings.Split(strings.Join(values, ","), ",")
		for i := len(chain) - 1; i >= 0; i-- {
			hop, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
			if err != nil {
				return peer
			}
			hop = hop.Unmap()
			if !trustedProxy(prefixes, hop) || i == 0 {
				return hop.String()
			}
		}
	}
	if real, err := netip.ParseAddr(strings.TrimSpace(request.Header.Get("X-Real-IP"))); err == nil {
		return real.Unmap().String()
	}
	return peer
}
