package agent

import (
	"bytes"
	"context"
	"path"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/airohq/airo-cli/internal/skill"
)

const (
	repo     = "https://git.example.com/org/skills"
	commit1  = "1111111111111111111111111111111111111111"
	commit2  = "2222222222222222222222222222222222222222"
	commitV1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// fakePin pins Git skills by version from heads, as a repository whose
// default branch and branches move.
func fakePin(t *testing.T, heads map[string]string) {
	t.Helper()
	old := pin
	t.Cleanup(func() { pin = old })
	pin = func(_ context.Context, s skill.Skill) (skill.Pinned, error) {
		p := skill.Pinned{Skill: s, Commit: heads[s.Version]}
		if p.Name == "" {
			p.Name = path.Base(s.Path)
		}
		return p, nil
	}
}

// ignoreNoStatus drops the error of a fake cluster that has no controller
// to make the Agent ready.
func ignoreNoStatus(err error) error {
	if err != nil && strings.Contains(err.Error(), "has no status") {
		return nil
	}
	return err
}

func agentWithSkills(skills []any, annotations map[string]string) *unstructured.Unstructured {
	inline := map[string]any{"systemPrompt": "You review."}
	if skills != nil {
		inline["skills"] = skills
	}
	a := resource(agentGVK, "my-agent", map[string]any{
		"harnessRef": map[string]any{"name": "main"},
		"template":   map[string]any{"inline": inline},
	})
	a.SetAnnotations(annotations)
	return a
}

func gitEntry(name, dir, commit string) map[string]any {
	return map[string]any{"name": name, "source": map[string]any{
		"git": map[string]any{"url": repo, "commit": commit}, "path": dir,
	}}
}

func skillsOf(t *testing.T, c client.Client) ([]any, map[string]string) {
	t.Helper()
	a := created(t, c)
	entries, _, err := unstructured.NestedSlice(a.Object, "spec", "template", "inline", "skills")
	if err != nil {
		t.Fatal(err)
	}
	return entries, versions(a)
}

func TestCreatePinsSkills(t *testing.T) {
	fakePin(t, map[string]string{"": commit1, "v1": commitV1})
	c := fake.NewClientBuilder().WithObjects(harness("main", "claude"), model("sonnet")).Build()
	out, err := run(t, c, options{prompt: "You review.", skills: []string{
		repo + "//skills/pdf", "docs=" + repo + "//skills/docs@v1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	entries, v := skillsOf(t, c)
	want := []any{gitEntry("pdf", "skills/pdf", commit1), gitEntry("docs", "skills/docs", commitV1)}
	if !equal(entries, want) {
		t.Errorf("skills = %v, want %v", entries, want)
	}
	if len(v) != 1 || v["docs"] != "v1" {
		t.Errorf("versions = %v, want only docs at v1", v)
	}
	if !strings.Contains(out, "skill:    pdf  "+repo+"//skills/pdf@"+commit1) {
		t.Errorf("output %q lacks the pinned pdf skill", out)
	}

	c = fake.NewClientBuilder().WithObjects(harness("main", "claude"), model("sonnet")).Build()
	if _, err := run(t, c, options{prompt: "x", skills: []string{repo + "//a/pdf", repo + "//b/pdf"}}); err == nil || !strings.Contains(err.Error(), "two skills are named pdf") {
		t.Errorf("create = %v, want an error for two skills of one name", err)
	}
}

func TestSkillAddUpgradeRemove(t *testing.T) {
	heads := map[string]string{"": commit1, "v1": commitV1}
	fakePin(t, heads)
	bucket := map[string]any{"name": "s3", "source": map[string]any{"bucket": map[string]any{"s3": map[string]any{"bucket": "b"}}}}
	c := fake.NewClientBuilder().WithObjects(agentWithSkills([]any{bucket}, nil)).Build()
	ctx := context.Background()
	var out bytes.Buffer

	if err := ignoreNoStatus(addSkill(ctx, &out, c, "team", "my-agent", repo+"//skills/pdf", 0)); err != nil {
		t.Fatal(err)
	}
	if err := ignoreNoStatus(addSkill(ctx, &out, c, "team", "my-agent", "docs="+repo+"//skills/docs@v1", 0)); err != nil {
		t.Fatal(err)
	}
	if err := addSkill(ctx, &out, c, "team", "my-agent", repo+"//other/pdf", 0); err == nil || !strings.Contains(err.Error(), "has a skill named pdf") {
		t.Errorf("add = %v, want an error for a name the agent has", err)
	}
	entries, v := skillsOf(t, c)
	if want := []any{bucket, gitEntry("pdf", "skills/pdf", commit1), gitEntry("docs", "skills/docs", commitV1)}; !equal(entries, want) {
		t.Fatalf("skills = %v, want %v", entries, want)
	}
	if len(v) != 1 || v["docs"] != "v1" {
		t.Errorf("versions = %v, want only docs at v1", v)
	}

	// The default branch moved: pdf follows it, docs stays at its tag.
	heads[""] = commit2
	out.Reset()
	if err := ignoreNoStatus(upgradeSkill(ctx, &out, c, "team", "my-agent", "pdf", "", 0)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "from:     "+repo+"//skills/pdf@"+commit1) || !strings.Contains(out.String(), "to:       "+repo+"//skills/pdf@"+commit2) {
		t.Errorf("upgrade output = %q", out.String())
	}
	out.Reset()
	if err := upgradeSkill(ctx, &out, c, "team", "my-agent", "docs", "", 0); err != nil || !strings.Contains(out.String(), "up to date with v1") {
		t.Errorf("upgrade of a tag = %v, output %q, want up to date", err, out.String())
	}
	// --version moves docs, and later upgrades follow it.
	heads["v2"] = commit2
	if err := ignoreNoStatus(upgradeSkill(ctx, &out, c, "team", "my-agent", "docs", "v2", 0)); err != nil {
		t.Fatal(err)
	}
	entries, v = skillsOf(t, c)
	if want := []any{bucket, gitEntry("pdf", "skills/pdf", commit2), gitEntry("docs", "skills/docs", commit2)}; !equal(entries, want) {
		t.Fatalf("skills = %v, want %v", entries, want)
	}
	if v["docs"] != "v2" {
		t.Errorf("versions = %v, want docs at v2", v)
	}
	if err := upgradeSkill(ctx, &out, c, "team", "my-agent", "s3", "", 0); err == nil || !strings.Contains(err.Error(), "does not read") {
		t.Errorf("upgrade of a bucket = %v, want an error", err)
	}
	if err := upgradeSkill(ctx, &out, c, "team", "my-agent", "nope", "", 0); err == nil || !strings.Contains(err.Error(), "it has docs, pdf, s3") {
		t.Errorf("upgrade of a missing skill = %v", err)
	}

	out.Reset()
	if err := listSkills(ctx, &out, &out, c, "team", "my-agent"); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"NAME", "pdf    default   " + repo, "docs   v2        " + repo, "s3     -         a source airo does not read"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("list output %q lacks %q", out.String(), line)
		}
	}

	for _, name := range []string{"docs", "pdf", "s3"} {
		if err := ignoreNoStatus(removeSkill(ctx, &out, c, "team", "my-agent", name, 0)); err != nil {
			t.Fatal(err)
		}
	}
	a := created(t, c)
	if _, found, _ := unstructured.NestedFieldNoCopy(a.Object, "spec", "template", "inline", "skills"); found {
		t.Error("the agent keeps an empty skills field")
	}
	if _, found := a.GetAnnotations()[skillVersionsAnnotation]; found {
		t.Error("the agent keeps the versions annotation")
	}
}

func TestSkillCommandsRefuseATemplateAgent(t *testing.T) {
	fakePin(t, map[string]string{"": commit1})
	a := resource(agentGVK, "my-agent", map[string]any{"template": map[string]any{"ref": map[string]any{"name": "shared"}}})
	c := fake.NewClientBuilder().WithObjects(a).Build()
	err := addSkill(context.Background(), &bytes.Buffer{}, c, "team", "my-agent", repo+"//pdf", 0)
	if err == nil || !strings.Contains(err.Error(), "AgentTemplate shared") {
		t.Errorf("add = %v, want an error for an agent with a template", err)
	}
	if err := listSkills(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, c, "team", "missing"); err == nil || !strings.Contains(err.Error(), "no agent named missing") {
		t.Errorf("list = %v, want an error for a missing agent", err)
	}
}
