package skill

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	mainCommit    = "1111111111111111111111111111111111111111"
	featureCommit = "2222222222222222222222222222222222222222"
	tagObject     = "3333333333333333333333333333333333333333"
	tagCommit     = "4444444444444444444444444444444444444444"
	digest        = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want Skill
	}{
		{"https://github.com/anthropics/skills/tree/main/skills/pdf",
			Skill{Git: "https://github.com/anthropics/skills", tree: "main/skills/pdf"}},
		{"https://github.com/anthropics/skills/blob/main/skills/pdf/SKILL.md",
			Skill{Git: "https://github.com/anthropics/skills", tree: "main/skills/pdf"}},
		{"https://github.com/anthropics/skills",
			Skill{Git: "https://github.com/anthropics/skills"}},
		{"https://git.example.com/team/skills.git//review@v1.2",
			Skill{Git: "https://git.example.com/team/skills.git", Path: "review", Version: "v1.2"}},
		{"https://git.example.com/team/skills@feature/x",
			Skill{Git: "https://git.example.com/team/skills", Version: "feature/x"}},
		{"docs=https://git.example.com/team/skills//a/b/",
			Skill{Name: "docs", Git: "https://git.example.com/team/skills", Path: "a/b"}},
		{"oci://ghcr.io/org/skills/pdf:1.0",
			Skill{OCI: "ghcr.io/org/skills/pdf", Version: "1.0"}},
		{"oci://localhost:5000/pdf",
			Skill{OCI: "localhost:5000/pdf"}},
		{"oci://ghcr.io/org/bundle@" + digest + "//skills/pdf",
			Skill{OCI: "ghcr.io/org/bundle", Version: digest, Path: "skills/pdf"}},

		// As npx skills takes them.
		{"anthropics/skills",
			Skill{Git: "https://github.com/anthropics/skills"}},
		{"anthropics/skills@frontend-design",
			Skill{Git: "https://github.com/anthropics/skills", Find: "frontend-design"}},
		{"anthropics/skills@v1.2.3",
			Skill{Git: "https://github.com/anthropics/skills", Version: "v1.2.3"}},
		{"anthropics/skills@v2",
			Skill{Git: "https://github.com/anthropics/skills", Find: "v2"}},
		{"anthropics/skills.git/skills/pdf",
			Skill{Git: "https://github.com/anthropics/skills", Path: "skills/pdf"}},
		{"anthropics/skills#dev@pdf",
			Skill{Git: "https://github.com/anthropics/skills", Version: "dev", Find: "pdf"}},
		{"anthropics/skills#@pdf",
			Skill{Git: "https://github.com/anthropics/skills", Find: "pdf"}},
		{"docs=github:org/repo#feature/x",
			Skill{Name: "docs", Git: "https://github.com/org/repo", Version: "feature/x"}},
		{"gitlab:group/repo/skills",
			Skill{Git: "https://gitlab.com/group/repo", Path: "skills"}},
		{"git@github.com:org/repo.git",
			Skill{Git: "https://github.com/org/repo.git"}},
		{"https://github.com/openai/skills#main@pdf",
			Skill{Git: "https://github.com/openai/skills", Version: "main", Find: "pdf"}},
		{"https://github.com/org/repo#@pdf",
			Skill{Git: "https://github.com/org/repo", Find: "pdf"}},
		{"github:org/repo@pdf",
			Skill{Git: "https://github.com/org/repo", Find: "pdf"}},
		{"org/repo@" + mainCommit,
			Skill{Git: "https://github.com/org/repo", Version: mainCommit}},
		{"org/repo@1.2",
			Skill{Git: "https://github.com/org/repo", Version: "1.2"}},
		{"org/repo@v1.2.3-rc.1",
			Skill{Git: "https://github.com/org/repo", Version: "v1.2.3-rc.1"}},
		{"org/repo/skills/pdf@pdf",
			Skill{Git: "https://github.com/org/repo", Path: "skills/pdf", Find: "pdf"}},
		{"git@gitlab.com:group/repo.git",
			Skill{Git: "https://gitlab.com/group/repo.git"}},
	} {
		got, err := Parse(tc.arg)
		if err != nil || got != tc.want {
			t.Errorf("Parse(%q) = %+v, %v, want %+v", tc.arg, got, err, tc.want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for arg, want := range map[string]string{
		"pdf":                           "cannot read the skill source",
		"./skills/pdf":                  "local directory",
		"org/repo@":                     "nothing after @",
		"org/repo#":                     "nothing after #",
		"org/":                          "give OWNER/REPO",
		"https://github.com/o/r@v1#dev": "a version twice",
		"https://git.example.com/o/r?x": "has a query",
		"https://user:secret@git.example.com/org/repo": "has credentials",
		"https://git.example.com/org/repo//../etc":     "leaves the repository",
		"https://git.example.com/org/repo@":            "empty version",
		"https://git.example.com":                      "no repository",
		"Bad_Name=https://git.example.com/org/repo":    "cannot name a skill",
		"oci://ghcr.io/org/pdf@sha256:abc":             "is not sha256",
		"https://github.com/org/repo/tree/":            "names no branch",
	} {
		if _, err := Parse(arg); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want an error with %q", arg, err, want)
		}
		if strings.Contains(arg, "secret") {
			if _, err := Parse(arg); strings.Contains(err.Error(), "secret") {
				t.Errorf("the error %q shows the password", err)
			}
		}
	}
}

// gitServer serves a repository's refs as Git's smart HTTP does.
func gitServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(serveRefs))
}

// serveRefs serves the refs of /org/skills.
func serveRefs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/org/skills/info/refs" || r.URL.Query().Get("service") != "git-upload-pack" {
		http.NotFound(w, r)
		return
	}
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	fmt.Fprint(w, pkt("# service=git-upload-pack\n")+"0000"+
		pkt(mainCommit+" HEAD\x00multi_ack symref=HEAD:refs/heads/main\n")+
		pkt(mainCommit+" refs/heads/main\n")+
		pkt(featureCommit+" refs/heads/feature/x\n")+
		pkt(tagObject+" refs/tags/v1.2\n")+
		pkt(tagCommit+" refs/tags/v1.2^{}\n")+"0000")
}

func TestPinGit(t *testing.T) {
	srv := gitServer(t)
	defer srv.Close()
	repo := srv.URL + "/org/skills"
	r := Resolver{Client: srv.Client()}
	for _, tc := range []struct {
		skill              Skill
		commit, name, path string
		version            string
	}{
		{Skill{Git: repo, Path: "skills/pdf"}, mainCommit, "pdf", "skills/pdf", ""},
		{Skill{Git: repo, Version: "feature/x"}, featureCommit, "skills", "", "feature/x"},
		{Skill{Git: repo, Version: "v1.2"}, tagCommit, "skills", "", "v1.2"},
		{Skill{Git: repo, Version: strings.ToUpper(featureCommit[:39]) + "A"}, strings.ToLower(featureCommit[:39]) + "a", "skills", "", strings.ToUpper(featureCommit[:39]) + "A"},
		{Skill{Git: repo, tree: "feature/x/skills/pdf"}, featureCommit, "pdf", "skills/pdf", "feature/x"},
		{Skill{Git: repo, tree: "main"}, mainCommit, "skills", "", "main"},
		{Skill{Git: repo, tree: tagCommit + "/a"}, tagCommit, "a", "a", tagCommit},
		{Skill{Name: "docs", Git: repo, Path: "skills/pdf"}, mainCommit, "docs", "skills/pdf", ""},
	} {
		got, err := r.Pin(context.Background(), tc.skill)
		if err != nil || got.Commit != tc.commit || got.Name != tc.name || got.Path != tc.path || got.Version != tc.version {
			t.Errorf("Pin(%+v) = %+v, %v, want commit %s, name %s, path %s, version %s", tc.skill, got, err, tc.commit, tc.name, tc.path, tc.version)
		}
	}
	for _, tc := range []struct {
		skill Skill
		want  string
	}{
		{Skill{Git: repo, Version: "nope"}, "no branch or tag nope"},
		{Skill{Git: repo, Version: "abc1234"}, "all its 40 characters"},
		{Skill{Git: repo, tree: "nope/skills"}, "no branch or tag at the start"},
		{Skill{Git: srv.URL + "/org/private"}, "private or does not exist"},
		{Skill{Git: repo, Path: "Skills_PDF"}, "NAME=SOURCE"},
	} {
		if _, err := r.Pin(context.Background(), tc.skill); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Pin(%+v) = %v, want an error with %q", tc.skill, err, tc.want)
		}
	}
}

func TestPinnedStringParsesBack(t *testing.T) {
	for _, p := range []Pinned{
		{Skill: Skill{Name: "pdf", Git: "https://github.com/org/skills", Path: "skills/pdf"}, Commit: mainCommit},
		{Skill: Skill{Name: "pdf", OCI: "ghcr.io/org/bundle", Path: "skills/pdf"}, Digest: digest},
	} {
		s, err := Parse(p.String())
		if err != nil || s.Git != p.Git || s.OCI != p.OCI || s.Path != p.Path || s.Version != p.Pin() {
			t.Errorf("Parse(%q) = %+v, %v, want %+v", p.String(), s, err, p)
		}
	}
}

// registry serves one tag behind an anonymous token, as ghcr.io and Docker
// Hub do.
func registry(t *testing.T, sendDigest bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.URL.Query().Get("scope") != "repository:org/pdf:pull" || r.URL.Query().Get("service") != "reg" {
				http.Error(w, "bad scope", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"token":"anon"}`)
		case "/v2/org/pdf/manifests/1.0":
			if r.Header.Get("Authorization") != "Bearer anon" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="reg"`, srv.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if sendDigest {
				w.Header().Set("Docker-Content-Digest", digest)
			}
			fmt.Fprint(w, "{}")
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func TestPinOCI(t *testing.T) {
	for _, sendDigest := range []bool{true, false} {
		srv := registry(t, sendDigest)
		host := strings.TrimPrefix(srv.URL, "https://")
		r := Resolver{Client: srv.Client()}
		got, err := r.Pin(context.Background(), Skill{OCI: host + "/org/pdf", Version: "1.0"})
		// sha256 of "{}", which a registry without the header is hashed to.
		want := map[bool]string{true: digest, false: "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"}[sendDigest]
		if err != nil || got.Digest != want || got.Name != "pdf" {
			t.Errorf("Pin with the digest header %v = %+v, %v, want %s", sendDigest, got, err, want)
		}
		if _, err := r.Pin(context.Background(), Skill{OCI: host + "/org/pdf", Version: "2.0"}); err == nil || !strings.Contains(err.Error(), "no such image or tag") {
			t.Errorf("Pin of a missing tag = %v", err)
		}
		srv.Close()
	}
	// A digest needs no registry.
	got, err := Resolver{}.Pin(context.Background(), Skill{OCI: "ghcr.io/org/pdf", Version: digest})
	if err != nil || got.Digest != digest {
		t.Errorf("Pin of a digest = %+v, %v", got, err)
	}
}

func TestSplitImage(t *testing.T) {
	for image, want := range map[string][2]string{
		"ghcr.io/org/pdf": {"ghcr.io", "org/pdf"},
		"localhost/pdf":   {"localhost", "pdf"},
		"host:5000/a/b":   {"host:5000", "a/b"},
		"org/pdf":         {"registry-1.docker.io", "org/pdf"},
		"pdf":             {"registry-1.docker.io", "library/pdf"},
	} {
		if r, repo := splitImage(image); r != want[0] || repo != want[1] {
			t.Errorf("splitImage(%q) = %s %s, want %v", image, r, repo, want)
		}
	}
}
