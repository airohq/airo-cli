package generic

import (
	"slices"
	"testing"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// A completion's requests are capped and never start a sign-in, and the
// kubeconfig's own exec settings are left as they are.
func TestQuietConfig(t *testing.T) {
	exec := &clientcmdapi.ExecConfig{
		Command:         "airo",
		Args:            []string{"credential", "--issuer=https://issuer.example"},
		InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
	}
	c := quietConfig(quietConfig(&rest.Config{ExecProvider: exec}))
	if c.Timeout != CompletionTimeout {
		t.Errorf("timeout = %v, want %v", c.Timeout, CompletionTimeout)
	}
	if got := c.ExecProvider; got.InteractiveMode != clientcmdapi.NeverExecInteractiveMode ||
		!slices.Equal(got.Args, []string{"credential", "--issuer=https://issuer.example", NoSignInFlag}) {
		t.Errorf("exec = %+v, want no interaction and %s once", got, NoSignInFlag)
	}
	if len(exec.Args) != 2 || exec.InteractiveMode != clientcmdapi.IfAvailableExecInteractiveMode {
		t.Errorf("the original exec config changed: %+v", exec)
	}

	// Another plugin gets no airo flag, but still no terminal.
	c = quietConfig(&rest.Config{ExecProvider: &clientcmdapi.ExecConfig{Command: "kubelogin", Args: []string{"get-token"}}})
	if got := c.ExecProvider; len(got.Args) != 1 || got.InteractiveMode != clientcmdapi.NeverExecInteractiveMode {
		t.Errorf("exec = %+v", got)
	}
}

func TestWithPrefix(t *testing.T) {
	got := WithPrefix([]string{"beta", "alpha", "alpine", "alpha"}, "al")
	if !slices.Equal(got, []string{"alpha", "alpine"}) {
		t.Errorf("WithPrefix = %q", got)
	}
}
