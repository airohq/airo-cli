package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps the tests off the kubeconfig of whoever runs them: a test
// that needs a cluster passes --kubeconfig, and help, which asks the cluster
// for short names, finds none.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "airo-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("KUBECONFIG", filepath.Join(dir, "none"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
