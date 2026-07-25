package share

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// LANAddresses lists assigned, global-unicast IPv4 addresses on interfaces
// that are currently up. Pier intentionally snapshots an address instead of
// following an interface across networks.
func LANAddresses() ([]Address, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	var out []Address
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, raw := range addrs {
			ip, _, err := net.ParseCIDR(raw.String())
			if err != nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, Address{Interface: iface.Name, IP: ip.String()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		vi, vj := virtualInterface(out[i].Interface), virtualInterface(out[j].Interface)
		if vi != vj {
			return !vi
		}
		if out[i].Interface != out[j].Interface {
			return out[i].Interface < out[j].Interface
		}
		return out[i].IP < out[j].IP
	})
	return out, nil
}

func virtualInterface(name string) bool {
	for _, prefix := range []string{"docker", "br-", "veth", "virbr", "podman", "cni"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// ResolveAddress validates --interface/--bind-ip against assigned addresses.
func ResolveAddress(addresses []Address, interfaceName, bindIP string) (Address, error) {
	if interfaceName != "" && bindIP != "" {
		return Address{}, fmt.Errorf("--interface and --bind-ip are mutually exclusive")
	}
	var matches []Address
	for _, address := range addresses {
		switch {
		case bindIP != "" && address.IP == bindIP:
			matches = append(matches, address)
		case interfaceName != "" && address.Interface == interfaceName:
			matches = append(matches, address)
		}
	}
	switch {
	case bindIP != "" && len(matches) == 0:
		return Address{}, fmt.Errorf("--bind-ip %s is not assigned to an active LAN interface", bindIP)
	case interfaceName != "" && len(matches) == 0:
		return Address{}, fmt.Errorf("--interface %s has no active LAN IPv4 address", interfaceName)
	case len(matches) > 1:
		return Address{}, fmt.Errorf("--interface %s has multiple IPv4 addresses; pass --bind-ip instead", interfaceName)
	case len(matches) == 1:
		return matches[0], nil
	default:
		return Address{}, fmt.Errorf("choose a LAN interface (pass --interface or --bind-ip)")
	}
}
