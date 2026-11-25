package ui

import (
	"testing"

	"github.com/spf13/cobra"
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

func TestRequirementsFlag(t *testing.T) {
	got := Requirements{Capabilities: []string{"open-tunnels"}}.Flag("let callers in without a token")
	if want := "let callers in without a token (required capabilities: open-tunnels)"; got != want {
		t.Fatalf("usage = %q, want %q", got, want)
	}
}
