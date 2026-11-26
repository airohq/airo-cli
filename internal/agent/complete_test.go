package agent

import (
	"context"
	"slices"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A skill completes from the Agent --agent names, or from the project's only
// one. With several and no --agent nothing completes: a skill of one agent
// does not exist in another that --agent names later.
func TestSkillChoices(t *testing.T) {
	ctx := context.Background()
	skills := []any{gitEntry("pdf", "skills/pdf", commit1), gitEntry("docs", "skills/docs", commit1)}
	one := fake.NewClientBuilder().WithObjects(agentWithSkills(skills, nil)).Build()
	if got := skillChoices(ctx, one, "team", "", ""); !slices.Equal(got, []string{"docs", "pdf"}) {
		t.Errorf("the only agent's skills = %q, want docs and pdf", got)
	}
	if got := skillChoices(ctx, one, "team", "my-agent", "p"); !slices.Equal(got, []string{"pdf"}) {
		t.Errorf("skills with p = %q, want pdf", got)
	}

	other := resource(agentGVK, "other", map[string]any{"template": map[string]any{"inline": map[string]any{}}})
	two := fake.NewClientBuilder().WithObjects(agentWithSkills(skills, nil), other).Build()
	if got := skillChoices(ctx, two, "team", "", ""); got != nil {
		t.Errorf("skills without --agent of two agents = %q, want none", got)
	}
	if got := skillChoices(ctx, two, "team", "my-agent", ""); !slices.Equal(got, []string{"docs", "pdf"}) {
		t.Errorf("skills of --agent my-agent = %q, want docs and pdf", got)
	}
	if got := skillChoices(ctx, two, "team", "missing", ""); got != nil {
		t.Errorf("skills of a missing agent = %q, want none", got)
	}
}
