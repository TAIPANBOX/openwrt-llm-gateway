package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
)

// ZoneSource tells which networks a firewall zone holds and which addresses a network has.
type ZoneSource interface {
	ZoneNetworks(zone string) ([]string, error)
	NetworkAddresses(network string) ([]netip.Addr, error)
}

// ListenAddrs is every address the gateway binds (invariant 4): the house addresses of the
// networks in the zones it names, on port, and 127.0.0.1 on loopbackPort. Never a wildcard.
// A zone that holds the wan network is refused, and a public address on a named zone (a
// delegated IPv6 prefix on the LAN, say) is skipped. A named zone with no address yet leaves
// loopback alone and an error that says so, so the service can start and log it.
func ListenAddrs(zones []string, src ZoneSource, port, loopbackPort int) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, z := range zones {
		nets, err := src.ZoneNetworks(z)
		if err != nil {
			return nil, err
		}
		for _, n := range nets {
			if n == "wan" || n == "wan6" {
				return nil, fmt.Errorf("zone %s holds the network %s: the gateway does not listen outside the house", z, n)
			}
			addrs, err := src.NetworkAddresses(n)
			if err != nil {
				return nil, fmt.Errorf("zone %s, network %s: %w", z, n, err)
			}
			for _, a := range addrs {
				if a.IsUnspecified() {
					return nil, fmt.Errorf("zone %s, network %s: reports the wildcard address %s", z, n, a)
				}
				if !inHouse(a) || a.IsLinkLocalUnicast() || a.IsLoopback() {
					continue
				}
				s := netip.AddrPortFrom(a.Unmap(), uint16(port)).String()
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
	}
	loop := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(loopbackPort)).String()
	if len(out) == 0 {
		return []string{loop}, fmt.Errorf("zones %s have no address in the house yet; listening on %s only", strings.Join(zones, ", "), loop)
	}
	return append(out, loop), nil
}

// openwrtZones reads zones from the firewall's UCI file and addresses from netifd over ubus.
type openwrtZones struct {
	firewall string
	ubus     func(args ...string) ([]byte, error)
}

func newOpenWrtZones() openwrtZones {
	return openwrtZones{firewall: "/etc/config/firewall", ubus: func(args ...string) ([]byte, error) {
		return exec.Command("ubus", args...).Output()
	}}
}

func (o openwrtZones) ZoneNetworks(zone string) ([]string, error) {
	f, err := os.Open(o.firewall)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	for _, s := range parseUCI(bufio.NewScanner(f)) {
		if s.Type != "zone" || s.Options["name"] != zone {
			continue
		}
		nets := append([]string(nil), s.Lists["network"]...)
		// Older configurations write the networks as one space-separated option.
		nets = append(nets, strings.Fields(s.Options["network"])...)
		return nets, nil
	}
	return nil, fmt.Errorf("no firewall zone named %s", zone)
}

func (o openwrtZones) NetworkAddresses(network string) ([]netip.Addr, error) {
	out, err := o.ubus("call", "network.interface."+network, "status")
	if err != nil {
		return nil, fmt.Errorf("ubus network.interface.%s status: %w", network, err)
	}
	return parseInterfaceStatus(out)
}

func parseInterfaceStatus(b []byte) ([]netip.Addr, error) {
	type addr struct {
		Address string `json:"address"`
	}
	var st struct {
		V4     []addr `json:"ipv4-address"`
		V6     []addr `json:"ipv6-address"`
		Assign []struct {
			Local addr `json:"local-address"`
		} `json:"ipv6-prefix-assignment"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	var out []netip.Addr
	add := func(s string) {
		if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, a)
		}
	}
	for _, a := range st.V4 {
		add(a.Address)
	}
	for _, a := range st.V6 {
		add(a.Address)
	}
	for _, a := range st.Assign {
		add(a.Local.Address)
	}
	return out, nil
}
