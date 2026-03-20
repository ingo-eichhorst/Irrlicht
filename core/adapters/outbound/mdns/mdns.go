// Package mdns advertises irrlichd via mDNS/Bonjour so it can be discovered
// on the local network without manual IP configuration.
package mdns

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/grandcat/zeroconf"
)

const (
	serviceType = "_irrlicht._tcp"
	domain      = "local."
)

// Advertiser broadcasts irrlichd's presence via mDNS.
type Advertiser struct {
	server *zeroconf.Server
}

// New creates and starts mDNS advertisement on the given port.
// The instance name defaults to the machine hostname.
func New(port int) (*Advertiser, error) {
	host, err := os.Hostname()
	if err != nil {
		host = "irrlichd"
	}
	// Strip domain suffixes that confuse mDNS responders.
	host = strings.TrimSuffix(host, ".local")

	// Collect local IPv4 addresses for the TXT record.
	addrs := localIPv4s()
	txt := []string{fmt.Sprintf("port=%d", port)}
	if len(addrs) > 0 {
		txt = append(txt, fmt.Sprintf("addr=%s", addrs[0]))
	}

	server, err := zeroconf.Register(host, serviceType, domain, port, txt, nil)
	if err != nil {
		return nil, fmt.Errorf("mdns: register: %w", err)
	}

	return &Advertiser{server: server}, nil
}

// Shutdown stops the mDNS advertisement. It blocks until the server sends
// mDNS goodbye packets or ctx is cancelled.
func (a *Advertiser) Shutdown(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		a.server.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// localIPv4s returns non-loopback IPv4 addresses on this machine.
func localIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				out = append(out, ip4.String())
			}
		}
	}
	return out
}
