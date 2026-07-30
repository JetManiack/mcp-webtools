package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestLivezIsUnconditional(t *testing.T) {
	rec := httptest.NewRecorder()
	Livez(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	okPing := func(context.Context) error { return nil }
	failPing := func(context.Context) error { return errors.New("connection refused") }

	tests := []struct {
		name        string
		checker     ReadyChecker
		wantStatus  int
		wantFailing []string
	}{
		{
			name:       "all good",
			checker:    ReadyChecker{Ping: okPing, MigrationsReady: true, OIDCReady: true},
			wantStatus: http.StatusOK,
		},
		{
			name:        "database down",
			checker:     ReadyChecker{Ping: failPing, MigrationsReady: true, OIDCReady: true},
			wantStatus:  http.StatusServiceUnavailable,
			wantFailing: []string{"database"},
		},
		{
			name:        "nothing ready yet",
			checker:     ReadyChecker{Ping: failPing},
			wantStatus:  http.StatusServiceUnavailable,
			wantFailing: []string{"migrations", "oidc_discovery", "database"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.checker.Readyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var body readyzResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body %q: %v", rec.Body.String(), err)
			}

			if tt.wantStatus == http.StatusOK {
				if body.Status != "ready" {
					t.Errorf("status field = %q, want ready", body.Status)
				}
				return
			}

			if body.Status != "not_ready" {
				t.Errorf("status field = %q, want not_ready", body.Status)
			}
			// The body has to name what's broken: a bare 503 tells an operator
			// nothing they can act on.
			for _, want := range tt.wantFailing {
				if !slices.Contains(body.Failing, want) {
					t.Errorf("failing = %v, want it to include %q", body.Failing, want)
				}
			}
		})
	}
}
