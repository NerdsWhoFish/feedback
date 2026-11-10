package config

import "testing"

func TestConfigFailsClosed(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("BASE_URL", "http://localhost:8080")
	t.Setenv("ENVIRONMENT", "local")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASE_URL", "http://feedback.example")
	if _, err := Load(); err == nil {
		t.Fatal("insecure public origin accepted")
	}
	t.Setenv("BASE_URL", "https://feedback.example/path")
	if _, err := Load(); err == nil {
		t.Fatal("non-origin base accepted")
	}
	t.Setenv("BASE_URL", "https://feedback.example")
	t.Setenv("ENVIRONMENT", "production")
	if _, err := Load(); err == nil {
		t.Fatal("production without OIDC accepted")
	}
}

func TestEndpointValidation(t *testing.T) {
	for _, value := range []string{"http://auth.example", "https://user:secret@auth.example", "https://auth.example?secret=x", "https://auth.example#fragment", "file:///etc/passwd"} {
		if ValidEndpoint(value, false) {
			t.Fatalf("accepted %s", value)
		}
	}
	if !ValidEndpoint("https://auth.example/oidc", false) {
		t.Fatal("HTTPS endpoint rejected")
	}
	if !ValidEndpoint("http://127.0.0.1:8081", true) {
		t.Fatal("local test provider rejected")
	}
}
