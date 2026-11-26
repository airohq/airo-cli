package ui

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestRequireStartsTheLongHelp(t *testing.T) {
	withLong := &cobra.Command{Short: "Short.", Long: "Long help."}
	withoutLong := &cobra.Command{Short: "Short."}
	Require(Requirements{Capabilities: []string{"agents", "tunnels"}, Solutions: []string{"adlc"}}, withLong, withoutLong)

	if want := "Required capabilities: agents, tunnels\nRequired solutions: adlc\n\nLong help."; withLong.Long != want {
		t.Fatalf("long help = %q, want %q", withLong.Long, want)
	}
	if want := "Required capabilities: agents, tunnels\nRequired solutions: adlc\n\nShort."; withoutLong.Long != want {
		t.Fatalf("long help without its own = %q, want %q", withoutLong.Long, want)
	}
}

func TestExplainOnlyForbidden(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "api.kagent.dev", Resource: "agents"}, "", errors.New("no grant"))
	wrapped := fmt.Errorf("list the agents of acme-web: %w", forbidden)

	got := Requirements{Capabilities: []string{"agents"}}.Explain("acme-web", "agents", wrapped)
	want := "project acme-web does not allow agents: ask a tenant administrator to enable the agents capability: " + wrapped.Error()
	if got == nil || got.Error() != want {
		t.Fatalf("error = %v, want %q", got, want)
	}
	if !apierrors.IsForbidden(got) {
		t.Fatal("the explanation lost the cluster's Forbidden")
	}

	other := errors.New("connection refused")
	if got := (Requirements{Capabilities: []string{"agents"}}).Explain("acme-web", "agents", other); got != other {
		t.Fatalf("a non-Forbidden error changed: %v", got)
	}
	if got := (Requirements{}).Explain("acme-web", "agents", nil); got != nil {
		t.Fatalf("nil became %v", got)
	}
}

func TestRequirementNames(t *testing.T) {
	for _, tc := range []struct {
		r    Requirements
		want string
	}{
		{Requirements{Capabilities: []string{"open-tunnels"}}, "the open-tunnels capability"},
		{Requirements{Capabilities: []string{"agents", "tunnels", "local-agents"}}, "the agents, tunnels and local-agents capabilities"},
		{Requirements{Capabilities: []string{"agents"}, Solutions: []string{"adlc"}}, "the agents capability and the adlc solution"},
	} {
		if got := tc.r.names(); got != tc.want {
			t.Errorf("names = %q, want %q", got, tc.want)
		}
	}
}

func TestRequirementsFlag(t *testing.T) {
	got := Requirements{Capabilities: []string{"open-tunnels"}}.Flag("let callers in without a token")
	if want := "let callers in without a token (required capabilities: open-tunnels)"; got != want {
		t.Fatalf("usage = %q, want %q", got, want)
	}
}
