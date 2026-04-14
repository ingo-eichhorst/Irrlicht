// Package httputil provides small HTTP-related helpers shared between adapters.
package httputil

import (
	"net"
	"net/http"
)

// IsLoopbackRequest reports whether the request came from the loopback
// interface or a Unix-domain socket. Unix-socket connections have an empty or
// non-host:port RemoteAddr, which we treat as trusted local IPC.
func IsLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
