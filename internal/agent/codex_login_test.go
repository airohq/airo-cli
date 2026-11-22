package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/airohq/kagent-remote-harness/runner/supervisor"
)

func TestCodexLoginSelection(t *testing.T) {
	name, err := codexVolumeName()
	if err != nil || name == "" {
		t.Fatal(name, err)
	}
	for _, tc := range []struct {
		harness, image, openAIKey, codexKey, want string
	}{
		{"codex", officialWorker, "", "", name},
		{"codex", loginImage, "", "", name},
		{"codex", officialWorker, "explicit", "", ""},
		{"codex", officialWorker, "", "explicit", ""},
		{"claude", officialWorker, "", "", ""},
		{"codex", "example/custom", "", "", ""},
	} {
		t.Setenv("OPENAI_API_KEY", tc.openAIKey)
		t.Setenv("CODEX_API_KEY", tc.codexKey)
		opts := supervisor.Options{Image: tc.image, WorkerOptions: supervisor.WorkerOptions{Harness: tc.harness}}
		got, err := workerCodexLogin(opts)
		if err != nil || got != tc.want {
			t.Fatalf("harness %s image %s: got %q, %v; want %q", tc.harness, tc.image, got, err, tc.want)
		}
	}
}

// Device login first clears its own auth file. A cancelled sign-in must
// leave the engine's working login intact. Run the Linux image script on CI.
func TestDeviceLoginPublishesOnlyOnSuccess(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the login image uses Linux flock and coreutils")
	}
	authDir := t.TempDir()
	credential := filepath.Join(authDir, "auth.json")
	if err := os.WriteFile(credential, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, success := range []bool{false, true} {
		script := `codex() {
			rm -f "$CODEX_HOME/auth.json"
			[ "$TEST_APPROVED" = yes ] || return 1
			printf approved > "$CODEX_HOME/auth.json"
		}
` + codexLoginScript
		cmd := exec.Command("sh", "-c", script, "codex-login", authDir)
		cmd.Env = append(os.Environ(), "CODEX_HOME="+t.TempDir(), "HOME="+t.TempDir())
		want := "old"
		if success {
			cmd.Env = append(cmd.Env, "TEST_APPROVED=yes")
			want = "approved"
		} else {
			cmd.Env = append(cmd.Env, "TEST_APPROVED=no")
		}
		out, err := cmd.CombinedOutput()
		if (err == nil) != success {
			t.Fatalf("login: %s, %v", out, err)
		}
		got, err := os.ReadFile(credential)
		if err != nil || string(got) != want {
			t.Fatalf("credential %q, want %q: %v", got, want, err)
		}
	}
}
