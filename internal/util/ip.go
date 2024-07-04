// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package util

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/samber/lo"
	"github.com/trim21/errgo"
)

func GetIpAddress() (*netip.Addr, *netip.Addr, error) {
	addrs, err := GetLocalIpaddress(nil)
	if err != nil {
		return nil, nil, err
	}

	var v4 *netip.Addr
	var v6 *netip.Addr

	for _, ips := range addrs {
		for _, ip := range ips {
			a, ok := netip.AddrFromSlice(ip)
			if !ok {
				continue
			}

			if a.Is4() {
				v4 = &a
			}

			if a.Is6() {
				v6 = &a
			}

			if v4 != nil && v6 != nil {
				return v4, v6, nil
			}
		}
	}

	return v4, v6, nil
}

func GetLocalIpaddress(enabledIf []string) (map[string][]net.IP, error) {
	ifces, err := net.Interfaces()
	if err != nil {
		return nil, errgo.Wrap(err, "failed to get network interfaces")
	}

	result := make(map[string][]net.IP, len(ifces))

	// handle err
	for _, i := range ifces {
		if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagPointToPoint != 0 {
			continue
		}

		if i.Flags&(net.FlagBroadcast|net.FlagMulticast) == 0 {
			continue
		}

		if len(enabledIf) != 0 {
			if !lo.Contains(enabledIf, i.Name) {
				continue
			}
		}

		addrs, err := i.Addrs()
		if err != nil {
			return nil, errgo.Wrap(err, fmt.Sprintf("failed to get address of net interface %s", i.Name))
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet: // ipv6 with prefix is a network
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}

			if isPrivateIP(ip) {
				continue
			}

			result[i.Name] = append(result[i.Name], ip)
		}
	}

	return result, nil
}

var privateIPBlocks []*net.IPNet

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // RFC3927 link-local
		"::1/128",        // IPv6 loopback
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique local addr
	} {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Errorf("parse error on %q: %v", cidr, err))
		}
		privateIPBlocks = append(privateIPBlocks, block)
	}
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return true
	}

	for _, block := range privateIPBlocks {
		if block.Contains(ip) {
			return true
		}
	}

	return false
}
