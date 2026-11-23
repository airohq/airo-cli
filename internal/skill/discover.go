package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"sigs.k8s.io/yaml"
)

// A skill's name is found as npx skills finds it (github.com/vercel-labs/
// skills, src/skills.ts, 2026-11): the name in the frontmatter of a
// directory's SKILL.md, compared without case, then as a slug. airo reads a
// GitHub repository's files through GitHub's REST API v3 (git trees) and
// raw.githubusercontent.com, without credentials, as the cluster fetches.
const (
	githubAPI = "https://api.github.com"
	githubRaw = "https://raw.githubusercontent.com"
	// maxDepth is how deep below the source's path a SKILL.md is looked
	// for, as npx skills searches a repository.
	maxDepth = 6
)

// skippedDirs hold no skills, as npx skills skips them.
var skippedDirs = map[string]bool{"node_modules": true, ".git": true, "dist": true, "build": true, "__pycache__": true}

// Found is a skill in a repository.
type Found struct {
	// Name is the name in the skill's SKILL.md, or else its directory's.
	Name string
	// Path is the skill's directory in the repository.
	Path string
}

// Choice is the error of Pin for a source that holds several skills and
// names none of them.
type Choice struct {
	// Source is the repository, at the path that holds the skills.
	Source string
	// Names are the skills' names, sorted.
	Names []string
}

func (c *Choice) Error() string {
	return fmt.Sprintf("%s holds %d skills, name one as OWNER/REPO@SKILL or give its directory: %s", c.Source, len(c.Names), strings.Join(c.Names, ", "))
}

// githubRepo returns the owner and the repository of a GitHub URL.
func githubRepo(git string) (owner, repo string, ok bool) {
	rest, found := strings.CutPrefix(git, "https://github.com/")
	if !found {
		return "", "", false
	}
	owner, repo, found = strings.Cut(strings.TrimSuffix(rest, ".git"), "/")
	return owner, repo, found && owner != "" && repo != "" && !strings.Contains(repo, "/")
}

// Page is GitHub's page of a directory of the repository at the pinned
// commit, or empty outside GitHub.
func (p Pinned) Page(dir string) string {
	owner, repo, ok := githubRepo(p.Git)
	if !ok || p.Commit == "" {
		return ""
	}
	page := "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/tree/" + p.Commit
	for part := range strings.SplitSeq(dir, "/") {
		if part != "" {
			page += "/" + url.PathEscape(part)
		}
	}
	return page
}

// locate finds the directory of the skill a pinned Git source means: the
// one named by Find, or the only one under its path. A path that holds a
// SKILL.md is the skill itself. Outside GitHub the path is taken as given.
func (r Resolver) locate(ctx context.Context, p Pinned) (Pinned, error) {
	owner, repo, ok := githubRepo(p.Git)
	if !ok {
		// Pin refused a name outside GitHub.
		return p, nil
	}
	if p.Find == "" {
		found, err := r.hasSkill(ctx, owner, repo, p.Commit, p.Path)
		if err != nil || found {
			return p, err
		}
	}
	skills, err := r.discover(ctx, owner, repo, p.Commit, p.Path, p.Find)
	if err != nil {
		return p, err
	}
	names := make([]string, 0, len(skills))
	for _, f := range skills {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	var chosen *Found
	switch {
	case len(skills) == 0:
		return p, fmt.Errorf("%s has no SKILL.md: give the directory of a skill", p.Location())
	case p.Find != "":
		for i := range skills {
			if SameName(skills[i].Name, p.Find) {
				chosen = &skills[i]
				break
			}
		}
		// A registry such as skills.sh may name a skill by its directory.
		for i := range skills {
			if chosen == nil && SameName(path.Base(skills[i].Path), p.Find) {
				chosen = &skills[i]
			}
		}
		if chosen == nil {
			return p, fmt.Errorf("%s has no skill named %s: it has %s", p.Location(), p.Find, strings.Join(names, ", "))
		}
	case len(skills) == 1:
		chosen = &skills[0]
	default:
		return p, &Choice{Source: p.Location(), Names: names}
	}
	p.Path, p.Find = chosen.Path, ""
	if p.Name == "" && ValidName(chosen.Name) == nil {
		p.Name = chosen.Name
	}
	return p, nil
}

// Skills pins a source in a GitHub repository and lists the skills under its
// path.
func (r Resolver) Skills(ctx context.Context, s Skill) (Pinned, []Found, error) {
	owner, repo, ok := githubRepo(s.Git)
	if !ok {
		return Pinned{}, nil, errors.New("airo lists the skills only of a GitHub repository")
	}
	p, err := r.pinGit(ctx, s)
	if err != nil {
		return p, nil, err
	}
	skills, err := r.discover(ctx, owner, repo, p.Commit, p.Path, "")
	return p, skills, err
}

// SameName compares names as npx skills does: without case, then as slugs.
func SameName(a, b string) bool {
	return strings.EqualFold(a, b) || slug(a) == slug(b)
}

var slugSeparators = regexp.MustCompile(`[\s_]+`)

func slug(name string) string {
	return slugSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
}

// hasSkill says whether the directory holds a SKILL.md.
func (r Resolver) hasSkill(ctx context.Context, owner, repo, commit, dir string) (bool, error) {
	resp, err := r.raw(ctx, http.MethodHead, owner, repo, commit, dir)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// raw requests dir/SKILL.md at the commit. A status other than 200 and 404
// is an error.
func (r Resolver) raw(ctx context.Context, method, owner, repo, commit, dir string) (*http.Response, error) {
	address := githubRaw + "/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/" + commit + "/" + path.Join(dir, "SKILL.md")
	req, err := http.NewRequestWithContext(ctx, method, address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path.Join(dir, "SKILL.md"), err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("read %s of github.com/%s/%s: %s", path.Join(dir, "SKILL.md"), owner, repo, resp.Status)
	}
	return resp, nil
}

// discover lists the skills under dir at the commit. With find, it reads
// first the directories named like find, and returns the first that is it.
func (r Resolver) discover(ctx context.Context, owner, repo, commit, dir, find string) ([]Found, error) {
	dirs, truncated, err := r.skillDirs(ctx, owner, repo, commit, dir)
	if err != nil {
		return nil, err
	}
	if truncated && len(dirs) == 0 {
		return nil, fmt.Errorf("github.com/%s/%s is too large to search for skills: give the directory of one", owner, repo)
	}
	if find != "" {
		for _, d := range dirs {
			if !SameName(path.Base(d), find) && (d != "" || !SameName(repo, find)) {
				continue
			}
			f, err := r.found(ctx, owner, repo, commit, d)
			if err != nil {
				return nil, err
			}
			if SameName(f.Name, find) {
				return []Found{f}, nil
			}
		}
	}

	// Each SKILL.md is read once, a few at a time.
	skills := make([]Found, len(dirs))
	errs := make([]error, len(dirs))
	limit := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, d := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			skills[i], errs[i] = r.found(ctx, owner, repo, commit, d)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	// The first of two with one name wins, as in npx skills.
	seen := map[string]bool{}
	unique := skills[:0]
	for _, f := range skills {
		if !seen[f.Name] {
			seen[f.Name] = true
			unique = append(unique, f)
		}
	}
	return unique, nil
}

// found reads the name of the skill in dir.
func (r Resolver) found(ctx context.Context, owner, repo, commit, dir string) (Found, error) {
	f := Found{Path: dir, Name: path.Base(dir)}
	if dir == "" {
		f.Name = repo
	}
	resp, err := r.raw(ctx, http.MethodGet, owner, repo, commit, dir)
	if err != nil {
		return f, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return f, fmt.Errorf("read %s: %w", path.Join(dir, "SKILL.md"), err)
	}
	if name := frontmatterName(data); name != "" {
		f.Name = name
	}
	return f, nil
}

// printable removes what could steer a terminal, since a skill's name and
// path come from whoever published it.
func printable(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s))
}

// frontmatterName returns the name in a SKILL.md's YAML frontmatter.
func frontmatterName(data []byte) string {
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	body, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return ""
	}
	end := strings.Index("\n"+body, "\n---")
	if end < 0 {
		return ""
	}
	var front struct {
		Name string `json:"name"`
	}
	if yaml.Unmarshal([]byte(body[:max(end-1, 0)]), &front) != nil {
		return ""
	}
	return printable(front.Name)
}

// skillDirs lists the directories under dir that hold a SKILL.md, the
// shallowest first. truncated is true when GitHub listed only part of the
// repository.
func (r Resolver) skillDirs(ctx context.Context, owner, repo, commit, dir string) (dirs []string, truncated bool, err error) {
	address := githubAPI + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/git/trees/" + commit + "?recursive=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("list the files of github.com/%s/%s: %w", owner, repo, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, false, fmt.Errorf("GitHub limits how often airo may list a repository's files without signing in: try again later, or give the skill's directory, such as https://github.com/%s/%s/tree/main/PATH", owner, repo)
	case resp.StatusCode != http.StatusOK:
		return nil, false, fmt.Errorf("list the files of github.com/%s/%s: %s", owner, repo, resp.Status)
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&tree); err != nil {
		return nil, false, fmt.Errorf("list the files of github.com/%s/%s: %w", owner, repo, err)
	}
	for _, entry := range tree.Tree {
		if entry.Type != "blob" || path.Base(entry.Path) != "SKILL.md" {
			continue
		}
		d := path.Dir(entry.Path)
		if d == "." {
			d = ""
		}
		if rel, ok := under(d, dir); ok && keep(rel) && printable(d) == d {
			dirs = append(dirs, d)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		if a, b := depth(dirs[i]), depth(dirs[j]); a != b {
			return a < b
		}
		return dirs[i] < dirs[j]
	})
	return dirs, tree.Truncated, nil
}

// under returns d relative to dir, if d is dir or below it.
func under(d, dir string) (string, bool) {
	switch {
	case dir == "":
		return d, true
	case d == dir:
		return "", true
	}
	rel, ok := strings.CutPrefix(d, dir+"/")
	return rel, ok
}

// keep says whether npx skills would search the relative directory.
func keep(rel string) bool {
	if rel == "" {
		return true
	}
	segments := strings.Split(rel, "/")
	if len(segments) > maxDepth {
		return false
	}
	for _, s := range segments {
		if skippedDirs[s] {
			return false
		}
	}
	return true
}

func depth(d string) int {
	if d == "" {
		return 0
	}
	return strings.Count(d, "/") + 1
}
