package registry

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// skills.sh search API, as npx skills find calls it (github.com/vercel-labs/
// skills, src/find.ts, 2026-11). The endpoint is not documented, and takes
// no credentials: GET /api/search?q=QUERY[&owner=OWNER][&limit=N] answers
// {"skills": [{"id", "source", "skillId", "name", "installs"}]}. skills.sh
// limits it by address, and its terms allow reasonable use.
const (
	skillsSHURL = "https://skills.sh"
	// MinQuery is the shortest query skills.sh answers.
	MinQuery = 2
	// fewResults and maxMerged are where npx skills find searches again
	// without each word of a query, and how many results it keeps then.
	fewResults = 10
	maxMerged  = 25
	maxWords   = 6
)

// SkillsSH searches skills.sh.
type SkillsSH struct {
	// Client is the HTTP client. Nil uses http.DefaultClient.
	Client *http.Client
	// URL is the address of skills.sh. Empty is https://skills.sh.
	URL string
}

func (s SkillsSH) Name() string { return "skills.sh" }

// Search asks skills.sh. Since it matches only skills with every word of
// the query, a query of a few words that finds few skills is asked again
// without each word, as npx skills find does, and the skills that match
// the most of the queries come first.
func (s SkillsSH) Search(ctx context.Context, query string, o Options) ([]Result, error) {
	query = strings.Join(strings.Fields(query), " ")
	if len(query) < MinQuery {
		return nil, fmt.Errorf("search for at least %d characters", MinQuery)
	}
	results, err := s.search(ctx, query, o)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(query)
	if len(results) >= fewResults || len(words) < 2 || len(words) > maxWords {
		return limit(results, o.Limit), nil
	}

	type merged struct {
		Result
		hits, rank int
	}
	byID := map[string]*merged{}
	var order []*merged
	add := func(results []Result) {
		for rank, r := range results {
			id := r.Repository + "@" + r.Skill
			m := byID[id]
			if m == nil {
				m = &merged{Result: r, rank: rank}
				byID[id] = m
				order = append(order, m)
			}
			m.hits++
			m.rank = min(m.rank, rank)
		}
	}
	add(results)
	for i := range words {
		fewer := strings.Join(slices.Delete(slices.Clone(words), i, i+1), " ")
		more, err := s.search(ctx, fewer, o)
		if err != nil {
			return nil, err
		}
		add(more)
	}
	slices.SortStableFunc(order, func(a, b *merged) int {
		return cmp.Or(cmp.Compare(b.hits, a.hits), cmp.Compare(a.rank, b.rank), cmp.Compare(b.Installs, a.Installs))
	})
	results = results[:0]
	for _, m := range order[:min(len(order), maxMerged)] {
		results = append(results, m.Result)
	}
	return limit(results, o.Limit), nil
}

func limit(results []Result, n int) []Result {
	if n > 0 && len(results) > n {
		return results[:n]
	}
	return results
}

func (s SkillsSH) search(ctx context.Context, query string, o Options) ([]Result, error) {
	q := url.Values{"q": {query}}
	if o.Owner != "" {
		q.Set("owner", o.Owner)
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cmp.Or(s.URL, skillsSHURL)+"/api/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search skills.sh: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Error  string `json:"error"`
		Skills []struct {
			Source   string `json:"source"`
			SkillID  string `json:"skillId"`
			Name     string `json:"name"`
			Installs *int64 `json:"installs"`
		} `json:"skills"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("skills.sh limits how often your address may search: try again later")
	case resp.StatusCode != http.StatusOK && body.Error != "":
		return nil, fmt.Errorf("search skills.sh: %s", clean(body.Error))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("search skills.sh: %s", resp.Status)
	case decodeErr != nil:
		return nil, fmt.Errorf("read the answer of skills.sh: %w", decodeErr)
	}
	results := make([]Result, 0, len(body.Skills))
	for _, s := range body.Skills {
		r := Result{Name: clean(s.Name), Repository: clean(s.Source), Skill: clean(s.SkillID), Installs: -1}
		if s.Installs != nil {
			r.Installs = *s.Installs
		}
		r.Name = cmp.Or(r.Name, r.Skill)
		if r.Repository == "" || r.Skill == "" {
			continue
		}
		results = append(results, r)
	}
	return results, nil
}

// clean removes what could steer a terminal, since the text comes from
// whoever published the skill.
func clean(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s))
}
