package config

import (
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// ValidOTLPEndpoint allows internal collectors to use HTTP.
func ValidOTLPEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if u.Scheme == "https" {
		return true
	}
	host := strings.ToLower(strings.TrimRight(u.Hostname(), "."))
	insecure := strings.EqualFold(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"), "true")
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Is6() && !strings.HasPrefix(u.Host, "[") {
			return false
		}
		ip = ip.Unmap()
		return ip.IsLoopback() || (insecure && (ip.IsPrivate() || ip.IsLinkLocalUnicast()))
	}
	if strings.ContainsAny(u.Host, "[]") {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	if len(host) > 253 {
		return false
	}
	// Reject legacy numeric IP spellings masquerading as service names.
	last := labels[len(labels)-1]
	if _, err := strconv.ParseUint(last, 0, 32); err == nil || strings.Trim(last, "0123456789") == "" {
		return false
	}
	if len(labels) == 1 || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local") {
		return true
	}
	if !insecure {
		return false
	}
	if strings.HasSuffix(host, ".cluster.local") {
		return true
	}
	// A service.namespace shorthand must not be a registrable public domain.
	// Prefer the .svc form for namespaces that collide with public suffixes.
	suffix, icann := publicsuffix.PublicSuffix(host)
	return len(labels) == 2 && !icann && suffix == labels[1]
}
