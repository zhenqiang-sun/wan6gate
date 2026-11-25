package system

import (
	"errors"
	"net"
	"strings"
)

// RouterIPv6 returns the global IPv6 GUA on pppoe-wan, or scans elsewhere as fallback.
// Returns "" with error if nothing global is found.
func RouterIPv6() (string, error) {
	if a, err := globalV6OnInterface("pppoe-wan"); err == nil && a != "" {
		return a, nil
	}
	// Fallback: scan all interfaces, skip lo + br-lan + docker
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifc := range ifaces {
		if ifc.Name == "lo" || strings.HasPrefix(ifc.Name, "br-lan") ||
			strings.HasPrefix(ifc.Name, "docker") || strings.HasPrefix(ifc.Name, "br-miot") {
			continue
		}
		if a, err := globalV6OnInterface(ifc.Name); err == nil && a != "" {
			return a, nil
		}
	}
	return "", errors.New("no global IPv6 found")
}

func globalV6OnInterface(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip.To4() != nil {
			continue
		}
		if ip.IsLinkLocalUnicast() || ip.IsLoopback() || ip.IsPrivate() {
			continue
		}
		if ip.IsGlobalUnicast() {
			return ip.String(), nil
		}
	}
	return "", nil
}
