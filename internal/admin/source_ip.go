package admin

import (
	"github.com/k0ngk0ng/stoneage/internal/clientip"
	"net/http"
	"net/netip"
)

func parseTrustedProxies(values []string) ([]netip.Prefix, error) {
	return clientip.ParseTrustedProxies(values)
}

func (server *Server) requestSourceIP(request *http.Request) string {
	return clientip.RequestSourceIP(request, server.trustedProxies)
}
