package bearer

import (
	"testing"

	"k8s.io/client-go/rest"
)

func TestToken(t *testing.T) {
	got, err := Token(&rest.Config{Host: "https://cluster.example.com", BearerToken: "abc"})
	if err != nil || got != "abc" {
		t.Errorf("Token = %q, %v; want abc", got, err)
	}
	if _, err := Token(&rest.Config{Host: "https://cluster.example.com"}); err == nil {
		t.Error("Token of a kubeconfig without a token: want an error")
	}
}
