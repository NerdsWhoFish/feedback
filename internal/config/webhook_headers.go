package config

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

const (
	maxWebhookHeaders     = 16
	maxWebhookHeaderBytes = 8 << 10
	maxWebhookHeadersJSON = 16 << 10
)

func ParseWebhookHeaders(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxWebhookHeadersJSON {
		return nil, errors.New("WEBHOOK_HEADERS exceeds the JSON size limit")
	}
	invalid := errors.New("WEBHOOK_HEADERS must be one JSON object of single string values")
	if !utf8.ValidString(raw) {
		return nil, invalid
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, invalid
	}
	headers := map[string]string{}
	// Map unmarshaling loses duplicate keys before they can be rejected.
	for decoder.More() {
		keyToken, err := decoder.Token()
		name, nameOK := keyToken.(string)
		if err != nil || !nameOK {
			return nil, invalid
		}
		valueToken, err := decoder.Token()
		value, valueOK := valueToken.(string)
		if err != nil || !valueOK {
			return nil, invalid
		}
		if err := addWebhookHeader(headers, name, value); err != nil {
			return nil, err
		}
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return nil, invalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalid
	}
	return headers, nil
}

// CopyWebhookHeaders validates programmatic callers and owns the returned map.
func CopyWebhookHeaders(input map[string]string) (map[string]string, error) {
	if len(input) > maxWebhookHeaders {
		return nil, errors.New("WEBHOOK_HEADERS exceeds the header count limit")
	}
	headers := make(map[string]string, len(input))
	for name, value := range input {
		if err := addWebhookHeader(headers, name, value); err != nil {
			return nil, err
		}
	}
	return headers, nil
}

func addWebhookHeader(headers map[string]string, name, value string) error {
	size := len(name) + len(value)
	for key, existing := range headers {
		size += len(key) + len(existing)
	}
	if len(headers) >= maxWebhookHeaders || size > maxWebhookHeaderBytes {
		return errors.New("WEBHOOK_HEADERS exceeds the header count or decoded size limit")
	}
	if !httpguts.ValidHeaderFieldName(name) || reservedWebhookHeader(name) {
		return errors.New("WEBHOOK_HEADERS contains an invalid or reserved header name")
	}
	name = http.CanonicalHeaderKey(name)
	if _, exists := headers[name]; exists {
		return errors.New("WEBHOOK_HEADERS contains duplicate header names")
	}
	if !utf8.ValidString(value) || !httpguts.ValidHeaderFieldValue(value) {
		return errors.New("WEBHOOK_HEADERS contains an invalid header value")
	}
	headers[name] = value
	return nil
}

func reservedWebhookHeader(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"x-feedback-", "x-b3-", "uberctx-", "ot-baggage-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "content-type", "host", "content-length", "transfer-encoding", "connection", "proxy-connection", "keep-alive", "te", "trailer", "upgrade", "expect", "content-encoding", "accept-encoding", "proxy-authorization", "proxy-authenticate",
		"traceparent", "tracestate", "baggage", "b3", "uber-trace-id", "x-amzn-trace-id", "x-cloud-trace-context", "grpc-trace-bin":
		return true
	}
	return false
}
