package origin

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// Normalize validates and canonicalizes an HTTP(S) origin.
func Normalize(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	u, err := url.Parse(value)
	if err != nil {
		return "", errors.New("invalid CORS origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") || u.Opaque != "" || value == "null" || strings.Contains(value, "*") {
		return "", errors.New("invalid CORS origin")
	}
	host := strings.ToLower(u.Hostname())
	isIPv6 := false
	if address, parseErr := netip.ParseAddr(host); parseErr == nil && address.Is6() {
		if address.Zone() != "" {
			return "", errors.New("invalid CORS origin host")
		}
		host = address.String()
		isIPv6 = true
	} else {
		asciiHost, idnaErr := idna.Lookup.ToASCII(host)
		if idnaErr != nil {
			return "", errors.New("invalid CORS origin host")
		}
		host = strings.ToLower(asciiHost)
	}
	port := u.Port()
	if port != "" {
		parsedPort, parseErr := strconv.Atoi(port)
		if parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", errors.New("invalid CORS origin port")
		}
		if (scheme == "https" && parsedPort != 443) || (scheme == "http" && parsedPort != 80) {
			port = strconv.Itoa(parsedPort)
		} else {
			port = ""
		}
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	if isIPv6 {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}
