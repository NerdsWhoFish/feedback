package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestWebhookHeadersRejectedAtStartup(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("BASE_URL", "http://localhost:8080")
	t.Setenv("ENVIRONMENT", "local")
	t.Setenv("WEBHOOK_URL", "")
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("WEBHOOK_HEADERS", `{"Authorization":null}`)
	if _, err := Load(); err == nil {
		t.Fatal("startup accepted a non-string webhook credential header")
	}
}

func TestWebhookAuthorizationShortcutAtStartup(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("BASE_URL", "http://localhost:8080")
	t.Setenv("ENVIRONMENT", "local")
	t.Setenv("WEBHOOK_URL", "")
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("WEBHOOK_HEADERS", "")
	t.Setenv("WEBHOOK_AUTHORIZATION", "<fake-raw-key>")
	c, err := Load()
	if err != nil || c.WebhookHeaders["Authorization"] != "<fake-raw-key>" {
		t.Fatal("startup did not preserve the authorization shortcut verbatim")
	}
	for _, value := range []string{"", "<fake-raw-key>", "Bearer <fake-prefixed-key>"} {
		t.Setenv("WEBHOOK_AUTHORIZATION", value)
		c, err = Load()
		_, present := c.WebhookHeaders["Authorization"]
		if err != nil || c.WebhookHeaders["Authorization"] != value || present != (value != "") {
			t.Fatal("shortcut changed the value or added an empty header")
		}
	}
	t.Setenv("WEBHOOK_AUTHORIZATION", "")
	t.Setenv("WEBHOOK_HEADERS", `{"aUtHoRiZaTiOn":"Bearer <fake-json-key>"}`)
	c, err = Load()
	if err != nil || c.WebhookHeaders["Authorization"] != "Bearer <fake-json-key>" {
		t.Fatal("empty shortcut conflicted with JSON authorization")
	}
}

func TestWebhookAuthorizationConflictsAndPrivacy(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("BASE_URL", "http://localhost:8080")
	t.Setenv("ENVIRONMENT", "local")
	t.Setenv("WEBHOOK_URL", "")
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("WEBHOOK_AUTHORIZATION", "<fake-shortcut-secret>")
	for _, name := range []string{"Authorization", "authorization", "AUTHORIZATION", "aUtHoRiZaTiOn"} {
		for _, value := range []any{"", "<fake-json-secret>", nil, []string{"<fake-json-secret>"}, 42} {
			raw, _ := json.Marshal(map[string]any{name: value})
			t.Setenv("WEBHOOK_HEADERS", string(raw))
			c, err := Load()
			if err == nil || len(c.WebhookHeaders) != 0 {
				t.Fatal("conflicting authorization sources accepted or returned")
			}
			if strings.Contains(err.Error(), "fake-") || strings.Contains(err.Error(), string(raw)) {
				t.Fatal("conflict error exposed credentials or JSON input")
			}
		}
	}
}

func TestWebhookAuthorizationValueValidation(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("BASE_URL", "http://localhost:8080")
	t.Setenv("ENVIRONMENT", "local")
	t.Setenv("WEBHOOK_URL", "")
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("WEBHOOK_HEADERS", "")
	// NUL cannot enter an environment value; shared header tests cover that byte.
	for _, value := range []string{"fake-private\r\nHost: bad", "fake-private\x1f", "fake-private\x7f", "fake-private\xff", strings.Repeat("fake", 2048)} {
		t.Setenv("WEBHOOK_AUTHORIZATION", value)
		c, err := Load()
		if err == nil || len(c.WebhookHeaders) != 0 {
			t.Fatal("unsafe authorization shortcut accepted or returned")
		}
		if !strings.Contains(err.Error(), "WEBHOOK_AUTHORIZATION") || strings.Contains(err.Error(), "fake") {
			t.Fatal("shortcut validation error was unclear or exposed a value")
		}
	}
	headers := map[string]string{}
	for i := range 16 {
		headers[fmt.Sprintf("X-Test-%d", i)] = "<fake-value>"
	}
	raw, _ := json.Marshal(headers)
	t.Setenv("WEBHOOK_HEADERS", string(raw))
	t.Setenv("WEBHOOK_AUTHORIZATION", "<fake-shortcut-secret>")
	if _, err := Load(); err == nil {
		t.Fatal("shortcut bypassed the combined header count limit")
	}
}

func TestParseWebhookHeaders(t *testing.T) {
	for _, raw := range []string{"", "{}", " \n {} \t"} {
		headers, err := ParseWebhookHeaders(raw)
		if err != nil || len(headers) != 0 {
			t.Fatal("empty headers must be supported")
		}
	}
	headers, err := ParseWebhookHeaders(`{"authorization":"Bearer <fake-sender-key>","x-routine-key":"<fake-custom-key>","X-Empty":""}`)
	if err != nil || len(headers) != 3 || headers["Authorization"] != "Bearer <fake-sender-key>" || headers["X-Routine-Key"] != "<fake-custom-key>" || headers["X-Empty"] != "" {
		t.Fatal("valid single-value headers were not canonicalized and preserved")
	}
	for i, raw := range []string{
		`null`, `[]`, `"fake-value"`, `true`, `1`, ` `,
		`{"Authorization":null}`, `{"Authorization":false}`, `{"Authorization":42}`, `{"Authorization":["fake-value"]}`, `{"Authorization":{"key":"fake-value"}}`,
		`{"Authorization":"fake-first","Authorization":"fake-second"}`, `{"authorization":"fake-first","AUTHORIZATION":"fake-second"}`, `{"Authorization":"fake-first","\u0041uthorization":"fake-second"}`,
		`{"X-Test":"fake-first","x-test":"fake-second"}`,
		`{"Authorization":"fake-value",}`, `{"Authorization":"fake-value"`, `{"Authorization":"fake-value"} {}`, `{} null`, `{} fake-secret-trailing`,
		`{"fake-secret-key":fake-secret-value}`, `{"fake-secret-key":"bad\q"}`, `{fake-secret-key:"fake-value"}`,
		"{\"X-Key\":\"\xff\"}",
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got, err := ParseWebhookHeaders(raw)
			if err == nil || got != nil {
				t.Fatal("invalid JSON accepted or partial credentials returned")
			}
			if !strings.HasPrefix(err.Error(), "WEBHOOK_HEADERS ") || strings.Contains(err.Error(), "fake-") || strings.Contains(err.Error(), "Authorization") {
				t.Fatal("validation error echoed input or decoder details")
			}
		})
	}
}

func TestWebhookHeaderNameGrammar(t *testing.T) {
	for i := 0; i < 256; i++ {
		name := string([]byte{byte(i)})
		want := i >= 'a' && i <= 'z' || i >= 'A' && i <= 'Z' || i >= '0' && i <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(i))
		headers, err := CopyWebhookHeaders(map[string]string{name: "fake-value"})
		if (err == nil) != want {
			t.Errorf("header name byte %d: accepted=%v, want %v", i, err == nil, want)
		}
		if want && headers[http.CanonicalHeaderKey(name)] != "fake-value" {
			t.Fatal("valid token header was changed")
		}
	}
	for _, name := range []string{"", " X-Key", "X-Key ", "X:Key", "X-Key\r\nHost", "X-雪"} {
		if _, err := CopyWebhookHeaders(map[string]string{name: "fake-value"}); err == nil {
			t.Fatal("invalid header name accepted")
		}
	}
}

func TestWebhookReservedHeaders(t *testing.T) {
	for _, name := range []string{
		"X-Feedback-Signature", "X-Feedback-Event", "X-Feedback-Delivery", "X-Feedback-Timestamp", "X-Feedback-Future", "Content-Type",
		"Host", "Content-Length", "Transfer-Encoding", "Connection", "Proxy-Connection", "Keep-Alive", "TE", "Trailer", "Upgrade", "Expect", "Content-Encoding", "Accept-Encoding",
		"Proxy-Authorization", "Proxy-Authenticate", "Traceparent", "Tracestate", "Baggage", "B3", "X-B3-TraceId", "X-B3-Future", "Uber-Trace-Id", "Uberctx-Credential", "Ot-Baggage-Credential", "X-Amzn-Trace-Id", "X-Cloud-Trace-Context", "Grpc-Trace-Bin",
	} {
		for _, variant := range []string{name, strings.ToLower(name), strings.ToUpper(name)} {
			raw, err := json.Marshal(map[string]string{variant: "fake-secret-value"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseWebhookHeaders(string(raw)); err == nil {
				t.Errorf("reserved header accepted: %s", name)
			} else if strings.Contains(err.Error(), "fake-secret-value") || strings.Contains(err.Error(), variant) {
				t.Fatal("reserved-header error exposed configuration")
			}
		}
	}
}

func TestWebhookHeaderValueControls(t *testing.T) {
	for i := 0; i <= 127; i++ {
		value := "fake" + string(rune(i)) + "value"
		raw, _ := json.Marshal(map[string]string{"Authorization": value})
		_, err := ParseWebhookHeaders(string(raw))
		want := i == '\t' || i >= 32 && i != 127
		if (err == nil) != want {
			t.Errorf("header value byte %d: accepted=%v, want %v", i, err == nil, want)
		}
	}
	if _, err := ParseWebhookHeaders(`{"X-Custom":"fake-雪"}`); err != nil {
		t.Fatal("valid UTF-8 field value rejected")
	}
}

func TestWebhookHeaderLimits(t *testing.T) {
	headers := map[string]string{}
	for i := range 16 {
		headers[fmt.Sprintf("X-Test-%d", i)] = "fake-value"
	}
	if _, err := CopyWebhookHeaders(headers); err != nil {
		t.Fatal("sixteen headers rejected")
	}
	headers["X-Too-Many"] = "fake-value"
	if _, err := CopyWebhookHeaders(headers); err == nil {
		t.Fatal("header count limit ignored")
	}
	for _, size := range []int{8191, 8192} {
		_, err := CopyWebhookHeaders(map[string]string{"X": strings.Repeat("a", size)})
		if (err == nil) != (size == 8191) {
			t.Fatal("combined decoded header size limit incorrect")
		}
	}
	if _, err := ParseWebhookHeaders("{}" + strings.Repeat(" ", 16382)); err != nil {
		t.Fatal("JSON input at the size limit rejected")
	}
	if _, err := ParseWebhookHeaders("{}" + strings.Repeat(" ", 16383)); err == nil {
		t.Fatal("JSON input size limit ignored")
	}
}

func TestWebhookHeaderCopyAndStartup(t *testing.T) {
	input := map[string]string{"authorization": "Bearer <fake-sender-key>"}
	owned, err := CopyWebhookHeaders(input)
	if err != nil || owned["Authorization"] != input["authorization"] || len(input) != 1 {
		t.Fatal("header copy failed")
	}
	if _, exists := input["Authorization"]; exists {
		t.Fatal("caller map was canonicalized in place")
	}
	input["authorization"] = "fake-replacement"
	if owned["Authorization"] != "Bearer <fake-sender-key>" {
		t.Fatal("caller mutation changed owned headers")
	}
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("BASE_URL", "http://localhost:8080")
	t.Setenv("ENVIRONMENT", "local")
	t.Setenv("WEBHOOK_URL", "")
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("WEBHOOK_HEADERS", `{"authorization":"Bearer <fake-sender-key>"}`)
	c, err := Load()
	if err != nil || c.WebhookHeaders["Authorization"] != "Bearer <fake-sender-key>" {
		t.Fatal("startup did not load headers while delivery was disabled")
	}
	t.Setenv("WEBHOOK_URL", "https://receiver.example/hook")
	t.Setenv("WEBHOOK_SECRET", "fake-signing-secret")
	c, err = Load()
	if err != nil || c.WebhookHeaders["Authorization"] != "Bearer <fake-sender-key>" {
		t.Fatal("startup did not load enabled delivery headers")
	}
	for _, raw := range []string{"", "{}"} {
		t.Setenv("WEBHOOK_HEADERS", raw)
		c, err = Load()
		if err != nil || len(c.WebhookHeaders) != 0 {
			t.Fatal("empty header configuration was not disabled")
		}
	}
}
