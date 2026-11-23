package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/airohq/airo-cli/internal/wizard"
)

// updatable is my-agent on harness main with model small and a skill, in a
// project that also has a harness byo and a model large.
func updatable(local bool) client.Client {
	a := agentWithSkills([]any{gitEntry("pdf", "skills/pdf", commit1)}, nil)
	_ = unstructured.SetNestedField(a.Object, "small", "spec", "template", "inline", "modelConfig", "name")
	_ = unstructured.SetNestedField(a.Object, "Reviews.", "spec", "template", "inline", "description")
	if local {
		a.SetLabels(map[string]string{localAgentLabel: "true"})
	}
	return fake.NewClientBuilder().WithObjects(a,
		harness("main", "claude"), harness("own", "byo"), model("small"), model("large")).Build()
}

func ptr(s string) *string { return &s }

func TestUpdateChangesOnlyWhatIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		local   bool
		ch      changes
		want    settings
		printed string
		err     string
	}{
		{name: "model", ch: changes{model: ptr("large")},
			want: settings{"You review.", "Reviews.", "main", "large"}, printed: "model:        large"},
		{name: "prompt and description", ch: changes{prompt: ptr("You test."), description: ptr("Tests.")},
			want: settings{"You test.", "Tests.", "main", "small"}, printed: "instructions: changed"},
		{name: "remove description", ch: changes{description: ptr("")},
			want: settings{"You review.", "", "main", "small"}, printed: "description:  removed"},
		{name: "byo harness without model", ch: changes{harness: ptr("own"), model: ptr("")},
			want: settings{"You review.", "Reviews.", "own", ""}, printed: "model:        removed"},
		{name: "same values", ch: changes{model: ptr("small"), harness: ptr("main")},
			want: settings{"You review.", "Reviews.", "main", "small"}, printed: "is unchanged"},
		{name: "unknown model", ch: changes{model: ptr("huge")}, err: "no model named huge: it has large, small"},
		{name: "unknown harness", ch: changes{harness: ptr("gone")}, err: "no harness named gone"},
		{name: "harness needs a model", ch: changes{model: ptr(""), harness: ptr("main")}, err: "needs a model"},
		{name: "local harness", local: true, ch: changes{harness: ptr("own")}, err: "is local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := updatable(tc.local)
			var out bytes.Buffer
			err := ignoreNoStatus(update(context.Background(), &out, c, "team", "my-agent", tc.ch, 0))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Errorf("update = %v, want an error with %q", err, tc.err)
				}
				return
			}
			a := created(t, c)
			got, _ := settingsOf(a)
			if err != nil || got != tc.want || !strings.Contains(out.String(), tc.printed) {
				t.Errorf("update = %v, settings %+v, printed %q; want %+v and %q", err, got, out.String(), tc.want, tc.printed)
			}
			if entries, _ := inlineSkills(a); len(entries) != 1 {
				t.Errorf("skills = %v, want the skill kept", entries)
			}
		})
	}
}

func TestUpdateWizardStartsFromTheCurrentValues(t *testing.T) {
	for _, tc := range []struct {
		name, answers string
		prompt        string // the current prompt
		edited        string // what the editor saves, for a prompt of lines
		want          settings
	}{
		// Enter keeps every value.
		{name: "keep all", answers: "\n\n\n\n", prompt: "You review.",
			want: settings{"You review.", "Reviews.", "main", "small"}},
		// A new description, and the second model of large and small.
		{name: "description and model", answers: "\nReviews PRs.\n\n1\n", prompt: "You review.",
			want: settings{"You review.", "Reviews PRs.", "main", "large"}},
		// Instructions of lines open in the editor.
		{name: "editor", answers: "y\n\n\n\n", prompt: "You review.\nCarefully.", edited: "You test.\n",
			want: settings{"You test.\n", "Reviews.", "main", "small"}},
		{name: "editor declined", answers: "\n\n\n\n", prompt: "You review.\nCarefully.", edited: "unused",
			want: settings{"You review.\nCarefully.", "Reviews.", "main", "small"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := updatable(false)
			a := created(t, c)
			_ = unstructured.SetNestedField(a.Object, tc.prompt, "spec", "template", "inline", "systemPrompt")
			if err := c.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			old := editText
			t.Cleanup(func() { editText = old })
			editText = func(text string) (string, error) {
				if text != tc.prompt {
					t.Errorf("editor opened %q, want %q", text, tc.prompt)
				}
				return tc.edited, nil
			}
			var shown bytes.Buffer
			w := wizard.Plain(context.Background(), strings.NewReader(tc.answers), &shown)
			ch, err := askChanges(context.Background(), w, c, "team", "my-agent")
			if err != nil {
				t.Fatalf("askChanges = %v, after %s", err, &shown)
			}
			var out bytes.Buffer
			if err := ignoreNoStatus(update(context.Background(), &out, c, "team", "my-agent", ch, 0)); err != nil {
				t.Fatal(err)
			}
			t.Logf("wizard:\n%s\nupdate:\n%s", &shown, &out)
			if got, _ := settingsOf(created(t, c)); got != tc.want {
				t.Errorf("settings %+v, want %+v; the wizard showed:\n%s", got, tc.want, &shown)
			}
		})
	}
}
