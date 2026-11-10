package config

import (
	"strings"
	"testing"
)

func TestOutgoingWebhookConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("BASE_URL", "http://localhost:8080")
	t.Setenv("ENVIRONMENT", "local")
	t.Setenv("WEBHOOK_URL", "")
	t.Setenv("WEBHOOK_SECRET", "")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBHOOK_URL", "https://receiver.example/hook")
	if _, err := Load(); err == nil {
		t.Fatal("configured outgoing webhook accepted without WEBHOOK_SECRET")
	}
	t.Setenv("WEBHOOK_SECRET", "test-only-signing-secret")
	if c, err := Load(); err != nil || c.WebhookURL != "https://receiver.example/hook" || c.WebhookSecret != "test-only-signing-secret" {
		t.Fatal("configured URL and secret were not loaded correctly")
	}
	for _, raw := range []string{"http://receiver.example/hook", "https://user:private@receiver.example", "https://receiver.example:65536", "https://receiver.example#fragment"} {
		t.Setenv("WEBHOOK_URL", raw)
		if _, err := Load(); err == nil {
			t.Errorf("unsafe endpoint accepted: %q", raw)
		} else if strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "test-only-signing-secret") {
			t.Fatal("configuration error exposed URL or secret")
		}
	}
}

func TestValidWebhookEndpoint(t *testing.T) {
	for _, insecure := range []string{"", "true"} {
		t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", insecure)
		for _, raw := range []string{
			"https://receiver.example/hook", "https://receiver.example:8443/hook",
			"http://localhost:8081/hook", "http://LOCALHOST.:8081/hook", "http://127.0.0.2/hook", "http://[::1]/hook", "http://[::ffff:127.0.0.1]/hook",
			"http://triage:8080/events", "http://triage.agents.svc/events", "http://TRIAGE.AGENTS.SVC.CLUSTER.LOCAL.:8080/events",
		} {
			if !ValidWebhookEndpoint(raw) {
				t.Errorf("rejected valid webhook endpoint %q", raw)
			}
		}
		for _, raw := range []string{
			"", "receiver.example/hook", "//receiver.example", "https:///hook", "http://receiver.example", "http://triage.agents", "http://evil.svc.example.com",
			"http://10.0.0.1", "http://169.254.169.254", "http://[fd00::1]", "http://0.0.0.0", "http://[::]", "http://8.8.8.8",
			"http://2130706433", "http://127.1", "http://0177.0.0.1", "http://0x7f000001", "https://127.1",
			"https://user:private@receiver.example", "https://receiver.example?secret=x", "https://receiver.example?", "https://receiver.example#", "https://receiver.example#fragment",
			"https://receiver.example:", "https://receiver.example:0", "https://receiver.example:65536", "https://receiver.example:abc", "https://receiver.example:-1",
			"https://bad..host", "https://bad_host", "https://-bad.example", "https://bad-.example", "https://receiver.example...", "https://" + strings.Repeat("a", 64) + ".example",
			"http://[triage]", "https://::1", "https://::1:443", "http://[fe80::1%25en0]", "https://receiver.example/%zz", "https://receiver.example/\n", "ftp://receiver.example",
		} {
			if ValidWebhookEndpoint(raw) {
				t.Errorf("accepted unsafe webhook endpoint %q (OTLP insecure=%q)", raw, insecure)
			}
		}
	}
}
