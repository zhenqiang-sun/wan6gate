package firewall

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// SectionName returns the uci section name for a (port, proto) tuple,
// e.g. "wan6gate_<port>_tcp".
func SectionName(port int, proto string) string {
	return fmt.Sprintf("wan6gate_%d_%s", port, strings.ToLower(proto))
}

// AddRule creates the uci section for an enabled mapping.
// Idempotent: deletes any existing section with the same name first.
func AddRule(port int, proto string) error {
	name := SectionName(port, proto)
	_ = uci("delete", "firewall."+name)
	cmds := [][]string{
		{"set", "firewall." + name + "=rule"},
		{"set", "firewall." + name + ".name=wan6gate " + strconv.Itoa(port) + "/" + proto},
		{"set", "firewall." + name + ".src=wan"},
		{"set", "firewall." + name + ".proto=" + proto},
		{"set", "firewall." + name + ".dest_port=" + strconv.Itoa(port)},
		{"set", "firewall." + name + ".family=ipv6"},
		{"set", "firewall." + name + ".target=ACCEPT"},
	}
	for _, c := range cmds {
		if err := uci(c...); err != nil {
			return fmt.Errorf("uci %v: %w", c, err)
		}
	}
	if err := uci("commit", "firewall"); err != nil {
		return fmt.Errorf("uci commit: %w", err)
	}
	return reloadFirewall()
}

// DeleteRule removes the section and reloads.
func DeleteRule(port int, proto string) error {
	name := SectionName(port, proto)
	if err := uci("delete", "firewall."+name); err != nil {
		// Treat "not found" as success
		s := err.Error()
		if !strings.Contains(s, "not found") &&
			!strings.Contains(s, "Entry not found") {
			return fmt.Errorf("uci delete: %w", err)
		}
	}
	if err := uci("commit", "firewall"); err != nil {
		return fmt.Errorf("uci commit: %w", err)
	}
	return reloadFirewall()
}

// CleanOrphans deletes any wan6gate_* sections not in the keep set.
// keep contains "<port>_<proto>" strings (no "wan6gate_" prefix).
func CleanOrphans(keep map[string]bool) error {
	list, err := listWan6gateSections()
	if err != nil {
		return err
	}
	changed := false
	for _, name := range list {
		suffix := strings.TrimPrefix(name, "wan6gate_")
		if !keep[suffix] {
			if err := uci("delete", "firewall."+name); err == nil {
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	if err := uci("commit", "firewall"); err != nil {
		return err
	}
	return reloadFirewall()
}

// DeleteAll removes every wan6gate_* section and reloads (no-op if none).
func DeleteAll() error {
	list, err := listWan6gateSections()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return nil
	}
	for _, name := range list {
		_ = uci("delete", "firewall."+name)
	}
	if err := uci("commit", "firewall"); err != nil {
		return err
	}
	return reloadFirewall()
}

// AddUILockdownRule installs an explicit DROP rule for WAN→tcp/<port>, used as
// belt-and-suspenders for the wan6gate UI port (the wildcard 0.0.0.0 bind by
// itself is already covered by zone_wan_input default REJECT, but this makes
// the intent explicit and survives any zone misconfig).
func AddUILockdownRule(uiPort int) error {
	name := fmt.Sprintf("wan6gate_ui_lockdown_%d", uiPort)
	_ = uci("delete", "firewall."+name)
	cmds := [][]string{
		{"set", "firewall." + name + "=rule"},
		{"set", "firewall." + name + ".name=wan6gate UI lockdown " + strconv.Itoa(uiPort)},
		{"set", "firewall." + name + ".src=wan"},
		{"set", "firewall." + name + ".proto=tcp"},
		{"set", "firewall." + name + ".dest_port=" + strconv.Itoa(uiPort)},
		{"set", "firewall." + name + ".family=any"},
		{"set", "firewall." + name + ".target=DROP"},
	}
	for _, c := range cmds {
		if err := uci(c...); err != nil {
			return fmt.Errorf("uci %v: %w", c, err)
		}
	}
	if err := uci("commit", "firewall"); err != nil {
		return err
	}
	return reloadFirewall()
}

// DeleteUILockdownRule removes lockdown rules for any port (matches by prefix).
func DeleteUILockdownRule() error {
	out, err := uciOut("show", "firewall")
	if err != nil {
		return err
	}
	changed := false
	seen := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "firewall.wan6gate_ui_lockdown") {
			continue
		}
		body := strings.TrimPrefix(line, "firewall.")
		eq := strings.Index(body, "=")
		if eq < 0 {
			continue
		}
		sec := body[:eq]
		if strings.Contains(sec, ".") {
			continue
		}
		if seen[sec] {
			continue
		}
		seen[sec] = true
		if err := uci("delete", "firewall."+sec); err == nil {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := uci("commit", "firewall"); err != nil {
		return err
	}
	return reloadFirewall()
}

// listWan6gateSections returns names like "wan6gate_<port>_<proto>".
func listWan6gateSections() ([]string, error) {
	out, err := uciOut("show", "firewall")
	if err != nil {
		return nil, err
	}
	var names []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "firewall.wan6gate_") {
			continue
		}
		body := strings.TrimPrefix(line, "firewall.")
		eq := strings.Index(body, "=")
		if eq < 0 {
			continue
		}
		sec := body[:eq]
		if strings.Contains(sec, ".") {
			// property line like firewall.wan6gate_<port>_<proto>.name='...'
			continue
		}
		if seen[sec] {
			continue
		}
		seen[sec] = true
		names = append(names, sec)
	}
	return names, nil
}

func uci(args ...string) error {
	cmd := exec.Command("/sbin/uci", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		s := strings.TrimSpace(stderr.String())
		if s == "" {
			return err
		}
		return fmt.Errorf("%s: %w", s, err)
	}
	return nil
}

func uciOut(args ...string) (string, error) {
	cmd := exec.Command("/sbin/uci", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}

func reloadFirewall() error {
	cmd := exec.Command("/etc/init.d/firewall", "reload")
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("firewall reload: %s: %w", strings.TrimSpace(combined.String()), err)
	}
	return nil
}
