// Package humanauth authenticates the humans who read tool-call history,
// as opposed to the agents that produce it (see internal/mcpserver).
package humanauth

import "net/http"

// Identity is the provider-agnostic result of authenticating a request.
type Identity struct {
	Subject     string
	DisplayName string
	Role        string
}

// Provider authenticates an incoming HTTP request and returns the identity
// making it.
type Provider interface {
	Authenticate(r *http.Request) (*Identity, error)
}

// StubProvider always authenticates the same fixed test identity, with NO
// real credential check whatsoever — no session, no cookie, no external
// call.
//
// ⚠️ It exists only so the app can be run locally without a Keycloak
// instance, and is reachable only via --auth-stub. Anything past a trusted
// dev machine must use the OIDC provider: tool-call history includes every
// URL and query an agent has ever sent, which is not data to serve to
// whoever reaches the port.
type StubProvider struct{}

func (StubProvider) Authenticate(r *http.Request) (*Identity, error) {
	return &Identity{
		Subject:     "stub-user",
		DisplayName: "Local Tester",
		Role:        "admin",
	}, nil
}
