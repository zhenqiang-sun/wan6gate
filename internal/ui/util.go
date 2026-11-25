package ui

import "wan6gate/internal/system"

// routerIPv6 is a tiny wrapper around system.RouterIPv6 used by handlers
// that need the current WAN GUA. Kept here so we can swap implementations
// (e.g. add a cache) without touching every call site.
func routerIPv6() (string, error) {
	return system.RouterIPv6()
}
