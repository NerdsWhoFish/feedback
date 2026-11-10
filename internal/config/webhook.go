package config

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// ValidWebhookEndpoint trusts an operator-configured destination, including
// loopback and cluster DNS over HTTP. OTLP's insecure opt-in does not apply.
func ValidWebhookEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.Opaque != "" {
		return false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	if strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || (ip.Is6() && !strings.HasPrefix(u.Host, "[")) {
			return false
		}
		return u.Scheme == "https" || ip.Unmap().IsLoopback()
	}
	if strings.ContainsAny(u.Host, "[]") || len(host) > 253 {
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
	// Numeric aliases can be interpreted as IPs by resolvers and HTTP stacks.
	last := labels[len(labels)-1]
	if _, err := strconv.ParseUint(last, 0, 32); err == nil || strings.Trim(last, "0123456789") == "" {
		return false
	}
	return u.Scheme == "https" || len(labels) == 1 || strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")
}
