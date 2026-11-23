// Package registry searches public directories of skills, such as
// skills.sh, for skills that airo agent skill add can add.
package registry

import (
	"context"
	"strings"
)

// Registry is a public directory of skills that answers searches.
type Registry interface {
	// Name is the registry's name, as people know it, such as skills.sh.
	Name() string
	// Search returns the skills that match the query, the most relevant
	// first.
	Search(ctx context.Context, query string, o Options) ([]Result, error)
}

// Options narrow a search.
type Options struct {
	// Owner keeps only the skills of this GitHub owner.
	Owner string
	// Limit is the most results to return. Zero is the registry's default.
	Limit int
}

// Result is a skill a registry found.
type Result struct {
	// Name is the skill's name, as the registry shows it.
	Name string
	// Repository is where the skill lives, such as anthropics/skills for a
	// GitHub repository, or a website such as developer.paddle.com.
	Repository string
	// Skill names the skill in the repository, as OWNER/REPO@SKILL takes it.
	Skill string
	// Installs is how many times the skill was installed, as the registry
	// counts, or -1 when it does not count.
	Installs int64
	// Page is the registry's web page about the skill, or empty.
	Page string
}

// OnGitHub says whether the skill lives in a GitHub repository, which airo
// can add. Others are published on a website, which airo cannot add yet.
func (r Result) OnGitHub() bool {
	owner, repo, ok := strings.Cut(r.Repository, "/")
	return ok && owner != "" && repo != "" && !strings.Contains(owner, ".") && !strings.Contains(repo, "/")
}

// Source is the skill as airo agent skill add takes it: OWNER/REPO@SKILL.
func (r Result) Source() string {
	return r.Repository + "@" + r.Skill
}
