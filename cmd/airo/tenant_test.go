package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTenantDetails(t *testing.T) {
	for _, tt := range []struct {
		name         string
		args         []string
		groups       []string
		token        string
		owner        string
		labels       map[string]string
		forbidden    bool
		wantFeatures string
		wantErr      string
	}{
		{
			name: "membership regardless of project", args: []string{"tenant", "-n", "unrelated"}, groups: []string{"customer:acme"}, token: "customer-token", owner: "acme",
			labels:       map[string]string{"features.airohq.dev/agents": "true", "features.airohq.dev/tunnels": "false", "features.airohq.dev/preview": "true", "customer": "true"},
			wantFeatures: "FEATURE STATE agents enabled preview enabled tunnels disabled",
		},
		{
			name: "explicit tenant", args: []string{"tenants", "--context", "admin", "--tenant", "other"},
			token: "admin-token", owner: "other",
			wantFeatures: "No feature labels.",
		},
		{
			name: "no tenant membership", args: []string{"tenant", "--context", "admin"}, token: "admin-token",
			wantErr: "has no tenant membership",
		},
		{
			name: "several memberships", args: []string{"tenant", "--context", "multi"}, token: "multi-token",
			groups: []string{"customer:acme", "customer:other"}, wantErr: "several tenants (acme, other): choose one with --tenant NAME",
		},
		{
			name: "tenant read denied", args: []string{"tenant", "--tenant", "other"}, token: "customer-token", owner: "other",
			forbidden: true, wantErr: "read tenant other: forbidden",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+tt.token {
					t.Errorf("unexpected credentials on %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "POST /apis/authentication.k8s.io/v1/selfsubjectreviews":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"apiVersion": "authentication.k8s.io/v1", "kind": "SelfSubjectReview",
						"status": map[string]any{"userInfo": map[string]any{"username": "user", "groups": tt.groups}},
					})
				case "GET /apis/capsule.clastix.io/v1beta2/tenants/" + tt.owner:
					if tt.forbidden {
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","message":"forbidden","code":403}`))
						return
					}
					meta := map[string]any{"name": tt.owner}
					if tt.labels != nil {
						meta["labels"] = tt.labels
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "capsule.clastix.io/v1beta2", "kind": "Tenant", "metadata": meta})
				default:
					t.Errorf("unexpected request for %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			out, err := runIdentityCommand(t, identityConfig(t, server.URL), tt.args...)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || out != "" {
					t.Fatalf("output %q, error %v, want %q", out, err, tt.wantErr)
				}
				return
			}
			want := "Tenant: " + tt.owner + " " + tt.wantFeatures
			if got := strings.Join(strings.Fields(out), " "); err != nil || got != want {
				t.Fatalf("output %q, error %v, want %q", got, err, want)
			}
		})
	}
}
