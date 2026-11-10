package config

import "testing"

func TestValidOTLPEndpoint(t *testing.T) {
	tests := []struct {
		url     string
		want    bool
		optedIn bool
	}{
		{"https://otlp.example.com/otlp", true, true},
		{"https://grafana.net", true, true},
		{"http://alloy.monitoring.svc:4318", true, true},
		{"http://alloy.monitoring.svc.cluster.local:4318", true, true},
		{"http://ALLOY.MONITORING.SVC.CLUSTER.LOCAL.:4318", true, true},
		{"http://ALLOY.MONITORING.SVC...:4318", true, true},
		{"http://alloy:4318", true, true},
		{"http://alloy.:4318", true, true},
		{"http://localhost:4318", true, true},
		{"http://LOCALHOST.:4318", true, true},
		{"http://127.0.0.1:4318", true, true},
		{"http://[::1]:4318", true, true},
		{"http://[::ffff:127.0.0.1]:4318", true, true},
		{"http://alloy.monitoring:4318", false, true},
		{"http://ALLOY.MONITORING.:4318", false, true},
		{"http://collector.cluster.local:4318", false, true},
		{"http://10.0.0.1:4318", false, true},
		{"http://172.16.0.1:4318", false, true},
		{"http://192.168.1.1:4318", false, true},
		{"http://169.254.1.1:4318", false, true},
		{"http://[fd00::1]:4318", false, true},
		{"http://[fe80::1]:4318", false, true},
		{"http://[::ffff:10.0.0.1]:4318", false, true},
		{"http://otlp.example.com", false, false},
		{"http://grafana.net", false, false},
		{"http://alloy.svc.evil.com", false, false},
		{"http://alloy.cluster.local.evil.com", false, false},
		{"http://alloy.dev", false, false},
		{"http://alloy.github.io", false, false},
		{"http://8.8.8.8:4318", false, false},
		{"http://8.8.8.8.:4318", false, false},
		{"http://8.8:4318", false, false},
		{"http://134744072:4318", false, false},
		{"http://0x08080808:4318", false, false},
		{"http://172.32.0.1:4318", false, false},
		{"http://[2606:4700:4700::1111]:4318", false, false},
		{"http://[::ffff:8.8.8.8]:4318", false, false},
		{"http://0.0.0.0:4318", false, false},
		{"http://[::]:4318", false, false},
		{"http://224.0.0.1:4318", false, false},
		{"", false, false},
		{"alloy:4318", false, false},
		{"http:///otlp", false, false},
		{"http://:4318", false, false},
		{"http://.", false, false},
		{"http://alloy..svc", false, false},
		{"http://-alloy.svc", false, false},
		{"http://alloy_svc", false, false},
		{"http://[::1", false, false},
		{"http://::1:4318", false, false},
		{"http://[alloy]:4318", false, false},
		{"http://alloy:abc", false, false},
		{"http://alloy:65536", false, false},
		{"http://alloy:0", false, false},
		{"http://alloy/%zz", false, false},
		{"http://user:secret@alloy", false, false},
		{"http://alloy?token=x", false, false},
		{"http://alloy?", false, false},
		{"http://alloy#fragment", false, false},
		{"http://alloy#", false, false},
		{"https://user:secret@alloy", false, false},
		{"https://alloy?token=x", false, false},
		{"https://alloy#fragment", false, false},
		{"ftp://alloy", false, false},
		{"file:///otlp", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			for _, optIn := range []string{"", "false", "true"} {
				t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", optIn)
				want := tt.want
				if optIn == "true" {
					want = tt.optedIn
				}
				if got := ValidOTLPEndpoint(tt.url); got != want {
					t.Errorf("insecure=%q: got %v, want %v", optIn, got, want)
				}
			}
		})
	}
}

func TestOTLPInsecureOptIn(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "True", "tRuE", "", "false", "1", "t", "yes", "on", " true ", "garbage"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", value)
			want := value == "true" || value == "TRUE" || value == "True" || value == "tRuE"
			if got := ValidOTLPEndpoint("http://alloy.monitoring:4318"); got != want {
				t.Errorf("got %v, want %v", got, want)
			}
			if ValidOTLPEndpoint("http://otlp.example.com") {
				t.Fatal("public HTTP accepted")
			}
		})
	}
}
