package skill

import (
	"bufio"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Resolver pins skills over HTTP, without credentials, as the cluster
// fetches them.
type Resolver struct {
	// Client is the HTTP client. Nil uses http.DefaultClient.
	Client *http.Client
}

func (r Resolver) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

// Pin returns the skill at the immutable version its Version names, and
// fills in the name and, for a GitHub page, the version and the path. In a
// GitHub repository, it finds the skill Find names, or the only skill under
// the path, and returns a *Choice for a path with several.
func (r Resolver) Pin(ctx context.Context, s Skill) (Pinned, error) {
	var p Pinned
	var err error
	if _, _, github := githubRepo(s.Git); s.Find != "" && !github {
		return p, fmt.Errorf("airo finds a skill by name only in a GitHub repository: give the directory of %s, as %s//PATH", s.Find, cmp.Or(s.Git, "oci://"+s.OCI))
	}
	if s.Git != "" {
		if p, err = r.pinGit(ctx, s); err == nil {
			p, err = r.locate(ctx, p)
		}
	} else {
		p, err = r.pinOCI(ctx, s)
	}
	if err != nil {
		return p, err
	}
	if p.Name == "" {
		p.Name = p.defaultName()
		if err := ValidName(p.Name); err != nil {
			return p, fmt.Errorf("%w. Name the skill with NAME=SOURCE", err)
		}
	}
	return p, nil
}

func (r Resolver) pinGit(ctx context.Context, s Skill) (Pinned, error) {
	if s.tree == "" && isCommit(s.Version) {
		return Pinned{Skill: s, Commit: strings.ToLower(s.Version)}, nil
	}
	if s.tree != "" {
		if first, rest, _ := strings.Cut(s.tree, "/"); isCommit(first) {
			s.Version, s.Path, s.tree = first, rest, ""
			return Pinned{Skill: s, Commit: strings.ToLower(first)}, nil
		}
	}
	refs, err := r.refs(ctx, s.Git)
	if err != nil {
		return Pinned{}, err
	}
	if s.tree != "" {
		// The longest prefix that is a branch or a tag is the ref, as GitHub
		// reads its own URLs.
		segments := strings.Split(s.tree, "/")
		for i := len(segments); i > 0; i-- {
			if _, ok := refs.lookup(strings.Join(segments[:i], "/")); ok {
				s.Version, s.Path = strings.Join(segments[:i], "/"), strings.Join(segments[i:], "/")
				break
			}
		}
		if s.Version == "" {
			return Pinned{}, fmt.Errorf("%s has no branch or tag at the start of %q", s.Git, s.tree)
		}
		s.tree = ""
	}
	if s.Version == "" {
		if refs.head == "" {
			return Pinned{}, fmt.Errorf("%s has no default branch: give a version with @VERSION", s.Git)
		}
		return Pinned{Skill: s, Commit: refs.head}, nil
	}
	commit, ok := refs.lookup(s.Version)
	if !ok {
		if shortCommit.MatchString(s.Version) {
			return Pinned{}, fmt.Errorf("%s has no branch or tag %s: give a commit with all its 40 characters", s.Git, s.Version)
		}
		return Pinned{}, fmt.Errorf("%s has no branch or tag %s", s.Git, s.Version)
	}
	return Pinned{Skill: s, Commit: commit}, nil
}

var shortCommit = regexp.MustCompile(`^[0-9a-fA-F]{4,39}$`)

// gitRefs are a repository's refs by full name, and the commit of HEAD.
type gitRefs struct {
	byName map[string]string
	head   string
}

// lookup finds a branch, then a tag at its commit, then a full ref name.
func (g gitRefs) lookup(version string) (string, bool) {
	for _, name := range []string{"refs/heads/" + version, "refs/tags/" + version + "^{}", "refs/tags/" + version, version} {
		if commit, ok := g.byName[name]; ok {
			return commit, true
		}
	}
	return "", false
}

// refs reads the repository's refs as git ls-remote does, from the smart
// HTTP ref advertisement of the Git protocol v0, which every Git host serves.
func (r Resolver) refs(ctx context.Context, repo string) (gitRefs, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(repo, "/")+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return gitRefs{}, err
	}
	// Some hosts answer a client that is not Git with a web page.
	req.Header.Set("User-Agent", "git/2 (airo)")
	resp, err := r.client().Do(req)
	if err != nil {
		return gitRefs{}, fmt.Errorf("read the branches and tags of %s: %w", repo, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return gitRefs{}, fmt.Errorf("cannot read %s: it is private or does not exist, and the cluster fetches skills only from public repositories", repo)
	case resp.StatusCode != http.StatusOK:
		return gitRefs{}, fmt.Errorf("read the branches and tags of %s: %s", repo, resp.Status)
	case resp.Header.Get("Content-Type") != "application/x-git-upload-pack-advertisement":
		return gitRefs{}, fmt.Errorf("%s is not a Git repository over HTTP", repo)
	}
	refs, err := parseAdvertisement(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return gitRefs{}, fmt.Errorf("read the branches and tags of %s: %w", repo, err)
	}
	return refs, nil
}

func parseAdvertisement(body io.Reader) (gitRefs, error) {
	refs := gitRefs{byName: map[string]string{}}
	in := bufio.NewReader(body)
	for {
		size := make([]byte, 4)
		if _, err := io.ReadFull(in, size); errors.Is(err, io.EOF) {
			return refs, nil
		} else if err != nil {
			return refs, err
		}
		n, err := strconv.ParseUint(string(size), 16, 16)
		if err != nil {
			return refs, fmt.Errorf("a malformed Git packet length %q", size)
		}
		if n == 0 {
			continue // a flush packet
		}
		if n < 4 {
			return refs, fmt.Errorf("a malformed Git packet length %q", size)
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(in, line); err != nil {
			return refs, err
		}
		text := strings.TrimSuffix(string(line), "\n")
		if strings.HasPrefix(text, "#") {
			continue // # service=git-upload-pack
		}
		text, _, _ = strings.Cut(text, "\x00") // the capabilities
		commit, name, ok := strings.Cut(text, " ")
		if !ok || !isCommit(commit) {
			continue
		}
		refs.byName[name] = commit
		if name == "HEAD" {
			refs.head = commit
		}
	}
}

// The manifests a registry may hold for a skill image.
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

func (r Resolver) pinOCI(ctx context.Context, s Skill) (Pinned, error) {
	if isDigest(s.Version) {
		return Pinned{Skill: s, Digest: s.Version}, nil
	}
	tag := s.Version
	if tag == "" {
		tag = "latest"
	}
	registry, repo := splitImage(s.OCI)
	address := "https://" + registry + "/v2/" + repo + "/manifests/" + url.PathEscape(tag)
	resp, err := r.manifest(ctx, http.MethodHead, address, repo, "")
	if err != nil {
		return Pinned{}, fmt.Errorf("read the image %s:%s: %w", s.OCI, tag, err)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	resp.Body.Close()
	if !isDigest(digest) {
		// A registry need not send the digest. The manifest's hash is it.
		resp, err = r.manifest(ctx, http.MethodGet, address, repo, "")
		if err != nil {
			return Pinned{}, fmt.Errorf("read the image %s:%s: %w", s.OCI, tag, err)
		}
		defer resp.Body.Close()
		h := sha256.New()
		if _, err := io.Copy(h, io.LimitReader(resp.Body, 4<<20)); err != nil {
			return Pinned{}, fmt.Errorf("read the image %s:%s: %w", s.OCI, tag, err)
		}
		digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	}
	return Pinned{Skill: s, Digest: digest}, nil
}

// manifest requests a manifest, and on 401 again with an anonymous token,
// as the registry's WWW-Authenticate header describes.
func (r Resolver) manifest(ctx context.Context, method, address, repo, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestTypes)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return resp, nil
	case resp.StatusCode == http.StatusUnauthorized && token == "":
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		token, err := r.anonymousToken(ctx, challenge, repo)
		if err != nil {
			return nil, err
		}
		return r.manifest(ctx, method, address, repo, token)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errors.New("the image is private: give its digest as IMAGE@sha256:..., if the cluster can pull it")
	case http.StatusNotFound:
		return nil, errors.New("no such image or tag")
	}
	return nil, errors.New(resp.Status)
}

func (r Resolver) anonymousToken(ctx context.Context, challenge, repo string) (string, error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", errors.New("the registry asks for credentials: give the image's digest as IMAGE@sha256:..., if the cluster can pull it")
	}
	values := map[string]string{}
	for _, part := range splitParams(params) {
		k, v, _ := strings.Cut(part, "=")
		values[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	realm, err := url.Parse(values["realm"])
	if err != nil || (realm.Scheme != "https" && realm.Scheme != "http") {
		return "", fmt.Errorf("the registry's token address %q is not a URL", values["realm"])
	}
	q := realm.Query()
	if values["service"] != "" {
		q.Set("service", values["service"])
	}
	scope := values["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the registry gave no anonymous token: %s", resp.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("read the registry's token: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", errors.New("the registry gave an empty token")
}

// splitParams splits a challenge's parameters at commas outside quotes.
func splitParams(s string) []string {
	var parts []string
	quoted, start := false, 0
	for i, c := range s {
		switch {
		case c == '"':
			quoted = !quoted
		case c == ',' && !quoted:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// splitImage returns the registry and the repository of an image, with
// Docker Hub's defaults for a name without a registry.
func splitImage(image string) (registry, repo string) {
	first, rest, found := strings.Cut(image, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first, rest
	}
	if !found {
		image = "library/" + image
	}
	return "registry-1.docker.io", image
}
