package restapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestMeReturnsIdentityAndRole(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, providerWithRole("viewer"))

	resp := do(t, server, http.MethodGet, "/api/me", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body meResponse
	decode(t, resp, &body)
	if body.Role != "viewer" {
		t.Errorf("Role = %q, want viewer", body.Role)
	}
	if body.DisplayName != "Tester (viewer)" {
		t.Errorf("DisplayName = %q, want the provider's display name", body.DisplayName)
	}
	if body.ActorID == "" {
		t.Error("ActorID is empty — the human actor was not provisioned")
	}
}

// An API client gets a 401 it can handle; only a browser navigation gets
// bounced to the login page. Redirecting an XHR just hands it an HTML page
// where it expected JSON.
func TestUnauthenticatedAPIRequestGets401(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, fakeProvider{err: errNoSession})

	resp := do(t, server, http.MethodGet, "/api/me", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestUnauthenticatedBrowserRequestRedirectsToLogin(t *testing.T) {
	db := openTestDB(t)
	server := newTestAPI(t, db, fakeProvider{err: errNoSession})

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/me", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Accept", "text/html")

	// Don't follow the redirect: the destination is what's under test.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if location := resp.Header.Get("Location"); !strings.HasSuffix(location, "/auth/login") {
		t.Errorf("Location = %q, want it to point at /auth/login", location)
	}
}
