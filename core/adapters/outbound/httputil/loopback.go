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

// IsCrossOriginBrowserRequest reports whether the request is a cross-origin
// browser request, per the Sec-Fetch-Site header. localhostOnly is not enough
// for mutating loopback routes: a safelisted cross-origin POST from any webpage
// the user visits reaches loopback without a CORS preflight and could drive a
// privileged action. Browsers stamp Sec-Fetch-Site; non-browser clients (the
// macOS URLSession client, curl) omit it and are treated as same-origin.
func IsCrossOriginBrowserRequest(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "cross-site" || site == "same-site"
}
