package system

import (
	"io"
	"os"
	"strconv"
	"strings"
)

// ListListening returns the set of TCP+UDP port numbers currently in LISTEN state.
// Reads /proc/net/tcp{,6} and /proc/net/udp{,6}. On non-Linux returns empty.
func ListListening() map[int]bool {
	out := make(map[int]bool)
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(f)
		f.Close()
		isTCP := strings.Contains(path, "tcp")
		for i, line := range strings.Split(string(b), "\n") {
			if i == 0 {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			local := fields[1]
			colon := strings.LastIndex(local, ":")
			if colon < 0 {
				continue
			}
			port, err := strconv.ParseInt(local[colon+1:], 16, 32)
			if err != nil {
				continue
			}
			// For TCP, only LISTEN (state 0A)
			if isTCP && fields[3] != "0A" {
				continue
			}
			out[int(port)] = true
		}
	}
	return out
}
