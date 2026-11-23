package skill

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// handlerTransport answers every request with a handler, whatever its host.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	return w.Result(), nil
}

// github serves github.com/org/skills at mainCommit: its refs, its tree
// and its SKILL.md files. lists counts the requests for the tree.
func github(t *testing.T, lists *int) Resolver {
	t.Helper()
	files := map[string]string{
		"skills/pdf/SKILL.md":                              "---\nname: pdf\ndescription: PDFs\n---\n# PDF",
		"skills/frontend/SKILL.md":                         "---\r\nname: \"frontend-design\"\r\n---\r\n",
		"plugins/k8s/skills/kubernetes-resources/SKILL.md": "---\nname: kubernetes-resources\n---\n",
		"node_modules/dep/SKILL.md":                        "---\nname: dep\n---\n",
		"a/b/c/d/e/f/g/SKILL.md":                           "---\nname: deep\n---\n",
		"skills/pdf/README.md":                             "",
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Host {
		case "github.com":
			serveRefs(w, r)
		case "api.github.com":
			if r.URL.Path != "/repos/org/skills/git/trees/"+mainCommit || r.URL.Query().Get("recursive") != "1" {
				http.NotFound(w, r)
				return
			}
			*lists++
			var tree []map[string]string
			for p := range files {
				tree = append(tree, map[string]string{"path": p, "type": "blob"})
			}
			tree = append(tree, map[string]string{"path": "skills", "type": "tree"})
			_ = json.NewEncoder(w).Encode(map[string]any{"tree": tree, "truncated": false})
		case "raw.githubusercontent.com":
			content, ok := files[strings.TrimPrefix(r.URL.Path, "/org/skills/"+mainCommit+"/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(content))
		default:
			http.NotFound(w, r)
		}
	})
	return Resolver{Client: &http.Client{Transport: handlerTransport{h}}}
}

func TestPinFindsASkillInAGitHubRepository(t *testing.T) {
	var lists int
	r := github(t, &lists)
	repo := "https://github.com/org/skills"
	for _, tc := range []struct {
		skill      Skill
		name, path string
		lists      int
	}{
		{Skill{Git: repo, Find: "frontend-design"}, "frontend-design", "skills/frontend", 1},
		{Skill{Git: repo, Find: "Frontend Design"}, "frontend-design", "skills/frontend", 1},
		{Skill{Git: repo, Find: "pdf"}, "pdf", "skills/pdf", 1},
		{Skill{Name: "docs", Git: repo, Find: "pdf"}, "docs", "skills/pdf", 1},
		{Skill{Git: repo, Find: "kubernetes-resources"}, "kubernetes-resources", "plugins/k8s/skills/kubernetes-resources", 1},
		// A path that holds a SKILL.md needs no listing.
		{Skill{Git: repo, Path: "skills/pdf"}, "pdf", "skills/pdf", 0},
		// The only skill under a path.
		{Skill{Git: repo, Path: "plugins/k8s"}, "kubernetes-resources", "plugins/k8s/skills/kubernetes-resources", 1},
	} {
		lists = 0
		got, err := r.Pin(context.Background(), tc.skill)
		if err != nil || got.Name != tc.name || got.Path != tc.path || got.Commit != mainCommit || got.Find != "" || lists != tc.lists {
			t.Errorf("Pin(%+v) = %+v, %v after %d listings, want %s at %s after %d", tc.skill, got, err, lists, tc.name, tc.path, tc.lists)
		}
	}

	_, err := r.Pin(context.Background(), Skill{Git: repo})
	var choice *Choice
	if !errors.As(err, &choice) || !reflect.DeepEqual(choice.Names, []string{"frontend-design", "kubernetes-resources", "pdf"}) {
		t.Errorf("Pin(a repository of several skills) = %v, want a choice of three", err)
	}
	for _, tc := range []struct {
		skill Skill
		want  string
	}{
		{Skill{Git: repo, Find: "dep"}, "no skill named dep: it has frontend-design, kubernetes-resources, pdf"},
		{Skill{Git: repo, Path: "docs"}, "has no SKILL.md"},
		{Skill{Git: srvlessRepo, Find: "pdf"}, "only in a GitHub repository"},
	} {
		if _, err := r.Pin(context.Background(), tc.skill); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Pin(%+v) = %v, want an error with %q", tc.skill, err, tc.want)
		}
	}

	_, skills, err := r.Skills(context.Background(), Skill{Git: repo, Path: "skills"})
	want := []Found{{Name: "frontend-design", Path: "skills/frontend"}, {Name: "pdf", Path: "skills/pdf"}}
	if err != nil || !reflect.DeepEqual(skills, want) {
		t.Errorf("Skills = %+v, %v, want %+v", skills, err, want)
	}
}

// srvlessRepo is a repository outside GitHub that no test serves: finding a
// skill there fails before anything is asked.
const srvlessRepo = "https://git.example.com/org/skills"

func TestFrontmatterName(t *testing.T) {
	for in, want := range map[string]string{
		"---\nname: pdf\n---\n":               "pdf",
		"\ufeff---\r\nname: 'a b'\r\n---\r\n": "a b",
		"---\n---\nname: no\n":                "",
		"# no frontmatter\nname: no\n":        "",
		"---\nname: [broken\n---\n":           "",
	} {
		if got := frontmatterName([]byte(in)); got != want {
			t.Errorf("frontmatterName(%q) = %q, want %q", in, got, want)
		}
	}
}
