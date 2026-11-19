package origin

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Normalize validates and canonicalizes an HTTP(S) origin.
func Normalize(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	u, err := url.Parse(value)
	if err != nil {
		return "", errors.New("invalid CORS origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || value == "null" || strings.Contains(value, "*") {
		return "", errors.New("invalid CORS origin")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		parsedPort, parseErr := strconv.Atoi(port)
		if parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", errors.New("invalid CORS origin port")
		}
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}
