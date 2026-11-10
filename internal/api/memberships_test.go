package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NerdsWhoFish/feedback/internal/auth"
)

func TestMembershipEmailValidation(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"yahoo", "jdfloraroofing@yahoo.com", "jdfloraroofing@yahoo.com"},
		{"normalized yahoo", " \tJdFloraRoofing@YAHOO.COM\r\n", "jdfloraroofing@yahoo.com"},
		{"gmail", "Member@gmail.com", "member@gmail.com"},
		{"custom domain", "Person@example.com", "person@example.com"},
		{"empty", " \t", ""},
		{"missing domain", "jdfloraroofing@", ""},
		{"missing local part", "@yahoo.com", ""},
		{"embedded space", "jd flora@yahoo.com", ""},
		{"display name", "JD Flora <jdfloraroofing@yahoo.com>", ""},
		{"multiple addresses", "one@yahoo.com,two@yahoo.com", ""},
		{"overlong", strings.Repeat("a", 245) + "@yahoo.com", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := email(tc.input)
			if tc.want == "" {
				var invalidEmail apiError
				if !errors.As(err, &invalidEmail) || invalidEmail.status != 400 || invalidEmail.code != "invalid_input" || got != "" {
					t.Fatalf("email(%q) = %q, %v; want invalid_input", tc.input, got, err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("email(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
}

func TestMembershipCreationRequiresAdmin(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name      string
		principal auth.Principal
	}{
		{"anonymous", auth.Principal{}},
		{"member", auth.Principal{UserID: "member", Email: "jdfloraroofing@yahoo.com", Role: "member"}},
		{"app with claimed admin role", auth.Principal{KeyID: "app", KeyType: "app", Role: "admin"}},
		{"assistant with claimed admin role", auth.Principal{KeyID: "assistant", KeyType: "assistant", Role: "admin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/projects/alpha/memberships", strings.NewReader(`{"email":"jdfloraroofing@yahoo.com"}`))
			r = r.WithContext(auth.WithPrincipal(r.Context(), tc.principal))
			if err := s.addMembership(httptest.NewRecorder(), r); !errors.Is(err, denied) {
				t.Fatalf("non-admin membership creation returned %v; want forbidden", err)
			}
		})
	}
}

func TestMembershipYahooEmailProjectAccess(t *testing.T) {
	f := testFixture(t)
	ctx := context.Background()
	const address = "jdfloraroofing@yahoo.com"
	member := auth.Principal{Email: strings.ToUpper(address), Name: "Yahoo member", Role: "member"}
	if err := f.db.QueryRow(ctx, `INSERT INTO users(email,name) VALUES($1,$2) RETURNING id::text`, address, member.Name).Scan(&member.UserID); err != nil {
		t.Fatal(err)
	}
	requests := "/api/v1/projects/" + f.project + "/requests"
	memberships := "/api/v1/projects/" + f.project + "/memberships"
	code, body := f.call(t, member, "GET", requests, nil)
	expectStatus(t, 404, code, body)

	for _, input := range []string{" \tJdFloraRoofing@YAHOO.COM\r\n", address} {
		code, body = f.call(t, f.admin, "POST", memberships, map[string]string{"email": input})
		expectStatus(t, 201, code, body)
		if body["email"] != address {
			t.Fatalf("membership email was not normalized: %v", body)
		}
	}
	code, body = f.call(t, f.admin, "GET", memberships, nil)
	expectStatus(t, 200, code, body)
	items := body["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["email"] != address {
		t.Fatalf("normalized membership missing or duplicated: %v", items)
	}
	code, body = f.call(t, member, "GET", "/api/v1/projects", nil)
	expectStatus(t, 200, code, body)
	items = body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != f.project {
		t.Fatalf("unexpected Yahoo member project visibility: %v", items)
	}
	code, body = f.call(t, member, "GET", requests, nil)
	expectStatus(t, 200, code, body)
	f.request(t, member, f.project, nil)
	code, body = f.call(t, member, "GET", "/api/v1/projects/"+f.other+"/requests", nil)
	expectStatus(t, 404, code, body)
	code, body = f.call(t, f.outsider, "GET", requests, nil)
	expectStatus(t, 404, code, body)
	for _, principal := range []auth.Principal{member, f.member, f.app, f.assistant} {
		code, body = f.call(t, principal, "POST", memberships, map[string]string{"email": "another@yahoo.com"})
		expectStatus(t, 403, code, body)
	}
	for _, input := range []string{"", "not-an-email", "JD Flora <jdfloraroofing@yahoo.com>", strings.Repeat("a", 245) + "@yahoo.com"} {
		code, body = f.call(t, f.admin, "POST", memberships, map[string]string{"email": input})
		expectStatus(t, 400, code, body)
	}
	code, body = f.call(t, f.admin, "POST", memberships, map[string]string{"email": address, "role": "admin"})
	expectStatus(t, 400, code, body)
	var role string
	if err := f.db.QueryRow(ctx, `SELECT role FROM users WHERE id=$1`, member.UserID).Scan(&role); err != nil || role != "member" {
		t.Fatalf("membership changed the user's role: role=%q err=%v", role, err)
	}
	code, body = f.call(t, f.admin, "DELETE", memberships+"/"+address, nil)
	expectStatus(t, 204, code, body)
	code, body = f.call(t, member, "GET", requests, nil)
	expectStatus(t, 404, code, body)
}
