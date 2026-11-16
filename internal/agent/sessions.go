package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// sessionStore remembers the last session with each agent, for --continue.
// It is a file in the user's cache directory: losing it loses no session,
// only the shortcut to it.
type sessionStore struct {
	path string
}

func newSessionStore() (sessionStore, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return sessionStore{}, err
	}
	return sessionStore{path: filepath.Join(dir, "airo", "sessions.json")}, nil
}

// sessionKey names an agent across clusters: host is the API server's URL.
func sessionKey(host, ns, agent string) string {
	return host + " " + ns + "/" + agent
}

func (s sessionStore) load() map[string]string {
	m := map[string]string{}
	if data, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	if m == nil {
		// A file that holds null.
		m = map[string]string{}
	}
	return m
}

// get returns the last session of key, or empty.
func (s sessionStore) get(key string) string {
	return s.load()[key]
}

// put makes id the last session of key.
func (s sessionStore) put(key, id string) error {
	m := s.load()
	m[key] = id
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
