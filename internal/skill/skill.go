// Package skill reads the skill sources people give airo and pins them to
// the immutable versions an Agent needs: a full Git commit or an OCI digest.
//
// A source is written as people copy it:
//
//	https://github.com/OWNER/REPO/tree/REF/PATH   a GitHub page
//	https://HOST/REPO[//PATH][@VERSION]           any Git host over HTTP(S)
//	oci://IMAGE[:TAG|@sha256:DIGEST][//PATH]      an OCI image
//
// or as the npx skills command takes it, from the skills CLI's source parser
// (github.com/vercel-labs/skills, src/source-parser.ts, 2026-11):
//
//	OWNER/REPO[/PATH][@SKILL|@VERSION]   a GitHub repository
//	OWNER/REPO#REF[@SKILL]               at a branch, a tag or a commit
//	github:OWNER/REPO, gitlab:OWNER/REPO the same on GitHub or GitLab
//	git@HOST:OWNER/REPO                  a repository by its SSH address
//
// with an optional NAME= in front. The cluster fetches skills without
// credentials, so a source must be public, or an image the cluster can pull.
package skill

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Skill is a skill as a person names it, before pinning.
type Skill struct {
	// Name is the skill's name in the Agent. Empty takes it from the source
	// when pinning.
	Name string
	// Git is the URL of a Git repository. Exactly one of Git and OCI is set.
	Git string
	// OCI is an image repository, without a tag or a digest.
	OCI string
	// Path is the skill's directory in the repository or the image.
	Path string
	// Version is what the person asked for: a branch, a tag, a commit, an
	// image tag or a digest. Empty follows the default branch, or the image
	// tag latest.
	Version string
	// Find is the name of a skill in a GitHub repository, as npx skills
	// takes it in OWNER/REPO@SKILL and --skill. Pinning finds the
	// directory whose SKILL.md has the name, and sets Path.
	Find string

	// tree is the rest of a GitHub page's URL: a ref and a path, which only
	// the repository's refs can tell apart, since a branch may have a slash.
	tree string
}

// Pinned is a skill at an immutable version.
type Pinned struct {
	Skill
	// Commit is the full Git commit, for a Git skill.
	Commit string
	// Digest is the image digest, such as sha256:..., for an OCI skill.
	Digest string
}

// Pin returns the immutable version, a commit or a digest.
func (p Pinned) Pin() string {
	if p.Git != "" {
		return p.Commit
	}
	return p.Digest
}

// Location is the source without a version.
func (s Skill) Location() string {
	if s.Git != "" {
		return join(s.Git, s.Path, "")
	}
	return join("oci://"+s.OCI, s.Path, "")
}

// String is the pinned source in the form Parse reads, so that a person can
// pass it back to --skill as it is.
func (p Pinned) String() string {
	if p.Git != "" {
		return join(p.Git, p.Path, "@"+p.Commit)
	}
	// An image's version goes before the path, as in IMAGE@DIGEST//PATH.
	return join("oci://"+p.OCI+"@"+p.Digest, p.Path, "")
}

func join(base, path, version string) string {
	if path != "" {
		base += "//" + path
	}
	return base + version
}

// The Agent Skills specification's names: lower-case letters, digits and
// single hyphens, at most 64 characters.
var validName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidName says why name cannot name a skill, or nil.
func ValidName(name string) error {
	if len(name) > 64 || !validName.MatchString(name) {
		return fmt.Errorf("%q cannot name a skill: use lower-case letters, digits and hyphens, at most 64 characters", name)
	}
	return nil
}

// Parse reads a source as --skill takes it.
func Parse(arg string) (Skill, error) {
	var s Skill
	arg = strings.TrimSpace(arg)
	// NAME=SOURCE. A URL's own = comes after a slash or a colon.
	if i := strings.Index(arg, "="); i > 0 && !strings.ContainsAny(arg[:i], "/:") {
		s.Name, arg = arg[:i], arg[i+1:]
		if err := ValidName(s.Name); err != nil {
			return s, err
		}
	}
	switch {
	case strings.HasPrefix(arg, "oci://"):
		return parseOCI(s, strings.TrimPrefix(arg, "oci://"))
	case strings.HasPrefix(arg, "https://"), strings.HasPrefix(arg, "http://"):
		return parseGit(s, arg)
	case strings.HasPrefix(arg, "github:"):
		return parseShorthand(s, strings.TrimPrefix(arg, "github:"), "github.com")
	case strings.HasPrefix(arg, "gitlab:"):
		return parseShorthand(s, strings.TrimPrefix(arg, "gitlab:"), "gitlab.com")
	case sshAddress.MatchString(arg):
		// The cluster fetches without credentials, so over HTTPS.
		m := sshAddress.FindStringSubmatch(arg)
		return parseGit(s, "https://"+m[1]+"/"+m[2])
	case strings.HasPrefix(arg, ".") || strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~"):
		return s, fmt.Errorf("the skill source %q is a local directory, which the cluster cannot fetch: push it to a public Git repository and give its URL", arg)
	case strings.Contains(arg, "/") && !strings.Contains(arg, ":"):
		return parseShorthand(s, arg, "github.com")
	}
	return s, fmt.Errorf("cannot read the skill source %q: give a GitHub repository such as OWNER/REPO@SKILL, a Git repository such as https://github.com/OWNER/REPO/tree/main/PATH or https://HOST/REPO//PATH@VERSION, or an image such as oci://IMAGE:TAG", arg)
}

// sshAddress is git@HOST:PATH, as a Git host shows a repository's SSH
// address.
var sshAddress = regexp.MustCompile(`^git@([^:/]+):([^:]+)$`)

// versionPin is what npx skills reads as a version in OWNER/REPO@X, rather
// than a skill's name: a semantic version or a full commit.
var versionPin = regexp.MustCompile(`^v?\d+(\.\d+)+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// parseShorthand reads OWNER/REPO[/PATH][#REF][@SKILL|@VERSION] on host.
func parseShorthand(s Skill, arg, host string) (Skill, error) {
	repo, fragment, hasRef := strings.Cut(arg, "#")
	var at string
	var hasAt bool
	if hasRef {
		// #@SKILL takes the default branch.
		s.Version, at, hasAt = strings.Cut(fragment, "@")
		s.Find = at
	} else if repo, at, hasAt = strings.Cut(repo, "@"); hasAt {
		if versionPin.MatchString(at) || isCommit(at) {
			s.Version = at
		} else {
			s.Find = at
		}
	}
	switch {
	case hasAt && at == "":
		return s, fmt.Errorf("the skill source %q has nothing after @", arg)
	case hasRef && s.Version == "" && s.Find == "":
		return s, fmt.Errorf("the skill source %q has nothing after #", arg)
	}
	parts := strings.SplitN(repo, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(repo, " \t?") {
		return s, fmt.Errorf("cannot read the skill source %q: give OWNER/REPO", arg)
	}
	s.Git = "https://" + host + "/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	if len(parts) == 3 {
		var err error
		if s.Path, err = cleanPath(parts[2]); err != nil {
			return s, err
		}
	}
	return s, nil
}

func parseGit(s Skill, arg string) (Skill, error) {
	u, err := url.Parse(arg)
	switch {
	case err != nil || u.Host == "":
		return s, fmt.Errorf("cannot read the skill source %q as a URL", arg)
	case u.User != nil:
		return s, fmt.Errorf("the skill source %s has credentials: remove them, the cluster fetches skills from public repositories without them", u.Redacted())
	case u.RawQuery != "":
		return s, fmt.Errorf("the skill source %q has a query: give the repository's URL", arg)
	case u.Fragment != "":
		// #REF[@SKILL], as npx skills takes it after a repository's URL.
		arg, _, _ = strings.Cut(arg, "#")
		if strings.Contains(arg, "@") || strings.Contains(u.Path, "/tree/") || strings.Contains(u.Path, "/blob/") || strings.Contains(u.Path, "//") {
			return s, fmt.Errorf("the skill source %q has a version twice: give it after # or in the URL", u.Redacted())
		}
		s.Version, s.Find, _ = strings.Cut(u.Fragment, "@")
		if s.Version == "" && s.Find == "" {
			return s, fmt.Errorf("the skill source %q has no branch, tag or commit after #", u.Redacted())
		}
	}
	scheme, rest, _ := strings.Cut(arg, "://")

	// A GitHub page, such as .../tree/main/skills/pdf or .../blob/main/skills/pdf/SKILL.md.
	if parts := strings.SplitN(rest, "/", 5); u.Host == "github.com" && len(parts) == 5 && (parts[3] == "tree" || parts[3] == "blob") {
		s.Git = scheme + "://" + strings.Join(parts[:3], "/")
		s.tree = strings.Trim(strings.TrimSuffix(parts[4], "SKILL.md"), "/")
		if s.tree == "" {
			return s, fmt.Errorf("the skill source %q names no branch, tag or commit after /%s/", arg, parts[3])
		}
		if _, err := cleanPath(s.tree); err != nil {
			return s, err
		}
		return s, nil
	}

	if i := strings.LastIndex(rest, "@"); i >= 0 {
		s.Version, rest = rest[i+1:], rest[:i]
		if s.Version == "" {
			return s, fmt.Errorf("the skill source %q has an empty version after @", arg)
		}
	}
	repo, dir, _ := strings.Cut(rest, "//")
	repo = strings.TrimSuffix(repo, "/")
	if !strings.Contains(repo, "/") {
		return s, fmt.Errorf("the skill source %q names a host and no repository", arg)
	}
	s.Git = scheme + "://" + repo
	s.Path, err = cleanPath(dir)
	return s, err
}

func parseOCI(s Skill, ref string) (Skill, error) {
	ref, dir, _ := strings.Cut(ref, "//")
	var err error
	if s.Path, err = cleanPath(dir); err != nil {
		return s, err
	}
	if i := strings.Index(ref, "@"); i >= 0 {
		s.Version, ref = ref[i+1:], ref[:i]
		if !isDigest(s.Version) {
			return s, fmt.Errorf("the image digest %q is not sha256: and 64 hexadecimal digits", s.Version)
		}
	} else if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		s.Version, ref = ref[i+1:], ref[:i]
	}
	if ref == "" || strings.ContainsAny(ref, " \t") {
		return s, fmt.Errorf("the skill source oci://%s names no image", ref)
	}
	s.OCI = ref
	return s, nil
}

// cleanPath returns a relative directory, or an error for one that leaves
// the repository.
func cleanPath(dir string) (string, error) {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return "", nil
	}
	for _, segment := range strings.Split(dir, "/") {
		if segment == ".." {
			return "", fmt.Errorf("the skill path %q leaves the repository", dir)
		}
	}
	if dir = path.Clean(dir); dir == "." {
		return "", nil
	}
	return dir, nil
}

// defaultName is the last directory of the path, or the repository's name.
func (s Skill) defaultName() string {
	name := path.Base(s.Path)
	if s.Path == "" {
		repo := s.Git
		if repo == "" {
			repo = s.OCI
		}
		name = strings.TrimSuffix(path.Base(repo), ".git")
	}
	return name
}

var (
	commitPattern = regexp.MustCompile(`^([0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func isCommit(v string) bool { return commitPattern.MatchString(v) }
func isDigest(v string) bool { return digestPattern.MatchString(v) }
