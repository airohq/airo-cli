package session

import (
	"context"
	"slices"
	"testing"
)

// Sessions complete newest first, with the first prompt to tell them apart,
// and an agent that does not answer leaves the others' sessions.
func TestSessionChoices(t *testing.T) {
	ctx := context.Background()
	api, _ := project(t, map[string]func(call) string{"reviewer": reviewer, "coder": coder, "broken": broken})
	want := []string{
		"s3\tcoder: Count slowly. Count slowly. Count slowly. Count s…",
		"s2\treviewer: Release it.",
		"s1\treviewer: Why does the build fail? See the log.",
	}
	if got := sessionChoices(ctx, api, "", false, ""); !slices.Equal(got, want) {
		t.Errorf("every agent's sessions =\n%q\nwant\n%q", got, want)
	}
	if got := sessionChoices(ctx, api, "reviewer", true, "s1"); !slices.Equal(got, []string{"s1\tWhy does the build fail? See the log."}) {
		t.Errorf("reviewer's sessions with s1 = %q", got)
	}
	// For chat, which talks to one agent, several agents and no --agent
	// offer nothing.
	if got := sessionChoices(ctx, api, "", true, ""); got != nil {
		t.Errorf("sessions for one agent of three = %q, want none", got)
	}

	api, _ = project(t, map[string]func(call) string{"coder": coder})
	if got := sessionChoices(ctx, api, "", true, ""); !slices.Equal(got, []string{"s3\tCount slowly. Count slowly. Count slowly. Count s…"}) {
		t.Errorf("the only agent's sessions = %q", got)
	}
}
