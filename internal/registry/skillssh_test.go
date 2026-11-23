package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type skill struct {
	Source   string `json:"source"`
	SkillID  string `json:"skillId"`
	Name     string `json:"name"`
	Installs int64  `json:"installs"`
}

// skillsSH answers each query with its skills, and counts the queries.
func skillsSH(t *testing.T, answers map[string][]skill, asked *[]string) SkillsSH {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query().Get("q")
		*asked = append(*asked, q+"|"+r.URL.Query().Get("owner")+"|"+r.URL.Query().Get("limit"))
		if q == "busy" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if q == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Query \x1b[31mis bad"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"query": q, "skills": answers[q]})
	}))
	t.Cleanup(srv.Close)
	return SkillsSH{Client: srv.Client(), URL: srv.URL}
}

func names(results []Result) []string {
	var out []string
	for _, r := range results {
		out = append(out, r.Source())
	}
	return out
}

func TestSkillsSHSearch(t *testing.T) {
	var asked []string
	s := skillsSH(t, map[string][]skill{
		"pdf": {
			{"anthropics/skills", "pdf", "pdf", 235455},
			{"developer.paddle.com", "paddle-billing", "Paddle \x1b]0;billing", 10},
			{"", "orphan", "orphan", 1},
		},
	}, &asked)
	got, err := s.Search(context.Background(), "  pdf ", Options{Owner: "anthropics", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(got), []string{"anthropics/skills@pdf", "developer.paddle.com@paddle-billing"}) {
		t.Errorf("results = %v", names(got))
	}
	if !got[0].OnGitHub() || got[1].OnGitHub() {
		t.Errorf("OnGitHub = %v, %v, want a repository and a website", got[0].OnGitHub(), got[1].OnGitHub())
	}
	if got[0].Page != "https://skills.sh/anthropics/skills/pdf" || got[1].Page != "" {
		t.Errorf("pages = %q, %q, want one for the repository's skill", got[0].Page, got[1].Page)
	}
	if got[1].Name != "Paddle ]0;billing" {
		t.Errorf("name = %q, want it without control characters", got[1].Name)
	}
	if !slices.Equal(asked, []string{"pdf|anthropics|5"}) {
		t.Errorf("asked %v", asked)
	}
}

func TestSkillsSHSearchesAgainWithoutEachWord(t *testing.T) {
	var asked []string
	s := skillsSH(t, map[string][]skill{
		"pdf forms": {{"a/skills", "pdf-forms", "pdf-forms", 1}},
		"forms":     {{"b/skills", "forms", "forms", 900}, {"a/skills", "pdf-forms", "pdf-forms", 1}},
		"pdf":       {{"c/skills", "pdf", "pdf", 5000}, {"a/skills", "pdf-forms", "pdf-forms", 1}, {"b/skills", "forms", "forms", 900}},
	}, &asked)
	got, err := s.Search(context.Background(), "pdf forms", Options{})
	if err != nil {
		t.Fatal(err)
	}
	// pdf-forms matches all three queries, forms two, pdf one.
	if want := []string{"a/skills@pdf-forms", "b/skills@forms", "c/skills@pdf"}; !slices.Equal(names(got), want) {
		t.Errorf("results = %v, want %v", names(got), want)
	}
	if len(asked) != 3 {
		t.Errorf("asked %v, want the query and once without each word", asked)
	}
}

func TestSkillsSHErrors(t *testing.T) {
	var asked []string
	s := skillsSH(t, nil, &asked)
	for query, want := range map[string]string{
		"p":    "at least 2 characters",
		"busy": "try again later",
		"bad":  "search skills.sh: Query [31mis bad",
	} {
		if _, err := s.Search(context.Background(), query, Options{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Search(%q) = %v, want an error with %q", query, err, want)
		}
	}
}
