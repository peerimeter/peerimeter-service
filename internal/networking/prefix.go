package networking

import (
	"encoding/json"
	"net/netip"
)

// NetworkPrefix validates and returns the canonical private IPv4 pool.
func NetworkPrefix(value json.RawMessage) (netip.Prefix, error) {
	var metadata struct {
		CIDR string `json:"cidr"`
	}
	if err := json.Unmarshal(value, &metadata); err != nil {
		return netip.Prefix{}, err
	}
	prefix, err := netip.ParsePrefix(metadata.CIDR)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() || prefix.Bits() < 8 || prefix.Bits() > 30 || !isPrivateIPv4(prefix) {
		return netip.Prefix{}, errInvalidPrefix
	}
	return prefix, nil
}

var errInvalidPrefix = &invalidPrefixError{}

type invalidPrefixError struct{}

func (*invalidPrefixError) Error() string { return "invalid private IPv4 network prefix" }

// NextAddress returns the first unused host address in a network.
func NextAddress(prefix netip.Prefix, used map[netip.Addr]bool) (netip.Addr, bool) {
	base := prefix.Masked().Addr().As4()
	first := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	last := first | uint32((1<<(32-prefix.Bits()))-1)
	for number := first + 1; number < last; number++ {
		address := netip.AddrFrom4([4]byte{byte(number >> 24), byte(number >> 16), byte(number >> 8), byte(number)})
		if !used[address] {
			return address, true
		}
	}
	return netip.Addr{}, false
}

func isPrivateIPv4(prefix netip.Prefix) bool {
	for _, raw := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		block := netip.MustParsePrefix(raw)
		if block.Contains(prefix.Addr()) && prefix.Bits() >= block.Bits() {
			return true
		}
	}
	return false
}
