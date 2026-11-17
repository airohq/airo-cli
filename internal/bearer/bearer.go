// Package bearer reads the signed-in person's token from the kubeconfig, for
// the commands that call a component's own API as that person.
package bearer

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
)

// Token returns the bearer token the kubeconfig sends to the API server,
// running its exec plugin if it has one. Call it again before each request
// or connection: the token expires.
func Token(cfg *rest.Config) (string, error) {
	tc, err := cfg.TransportConfig()
	if err != nil {
		return "", err
	}
	var captured string
	rt, err := transport.HTTPWrappersForConfig(tc, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		captured = r.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}))
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequest(http.MethodGet, "https://kubernetes.invalid/", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		return "", err
	}
	tok, ok := strings.CutPrefix(captured, "Bearer ")
	if !ok || tok == "" {
		return "", errors.New("the kubeconfig's user has no bearer token: sign in with a token or an exec plugin, not a client certificate")
	}
	return tok, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
