package clientip

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// ProxyHeader carries only audit metadata. Routing remains gateway-owned.
func ProxyHeader(source string) ([]byte, error) {
	ip, err := netip.ParseAddr(source)
	if err != nil {
		return nil, fmt.Errorf("invalid client IP: %w", err)
	}
	ip = ip.Unmap()
	family, destination := "TCP4", "0.0.0.0"
	if ip.Is6() {
		family, destination = "TCP6", "::"
	}
	return []byte(fmt.Sprintf("PROXY %s %s %s 0 0\r\n", family, ip, destination)), nil
}

func ParseProxyHeader(packet []byte) (string, error) {
	if len(packet) > 108 || !strings.HasSuffix(string(packet), "\r\n") {
		return "", fmt.Errorf("invalid PROXY header length or terminator")
	}
	fields := strings.Fields(string(packet))
	if len(fields) != 6 || fields[0] != "PROXY" || (fields[1] != "TCP4" && fields[1] != "TCP6") {
		return "", fmt.Errorf("invalid PROXY header fields")
	}
	source, err := netip.ParseAddr(fields[2])
	destination, destErr := netip.ParseAddr(fields[3])
	if err != nil || destErr != nil || source.Zone() != "" || destination.Zone() != "" || source.Is4() != (fields[1] == "TCP4") || destination.Is4() != (fields[1] == "TCP4") {
		return "", fmt.Errorf("invalid PROXY IP addresses")
	}
	for _, value := range fields[4:] {
		if _, err := strconv.ParseUint(value, 10, 16); err != nil {
			return "", fmt.Errorf("invalid PROXY port")
		}
	}
	return source.Unmap().String(), nil
}
