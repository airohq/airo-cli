package login

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func jwt(exp time.Time) string {
	payload, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestExpiry(t *testing.T) {
	want := time.Now().Add(time.Hour).Truncate(time.Second)
	if got, err := expiry(jwt(want)); err != nil || !got.Equal(want) {
		t.Errorf("expiry = %v, %v; want %v", got, err, want)
	}
	for _, bad := range []string{"", "a.b", "a.!!.c", "e30.e30.sig"} {
		if _, err := expiry(bad); err == nil {
			t.Errorf("expiry(%q) gave no error", bad)
		}
	}
}

func TestOIDCFrom(t *testing.T) {
	want := oidc{issuer: "https://issuer.example/", clientID: "abc"}
	for name, args := range map[string][]string{
		"kubelogin": {"oidc-login", "get-token", "--grant-type=device-code", "--oidc-issuer-url=https://issuer.example/", "--oidc-client-id=abc"},
		"airo":      execUser(want, "").Exec.Args,
	} {
		got, err := oidcFrom(&clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{Args: args}})
		if err != nil || got != want {
			t.Errorf("%s: oidcFrom = %+v, %v", name, got, err)
		}
	}
	for name, user := range map[string]*clientcmdapi.AuthInfo{
		"nil":   nil,
		"token": {Token: "x"},
		"other": {Exec: &clientcmdapi.ExecConfig{Args: []string{"get-token"}}},
	} {
		if _, err := oidcFrom(user); err == nil {
			t.Errorf("%s: oidcFrom gave no error", name)
		}
	}
}

func TestDefaultProject(t *testing.T) {
	if got := defaultProject([]string{"system:authenticated", "customer:acme", "customer:other"}); got != "acme-default" {
		t.Errorf("defaultProject = %q", got)
	}
	if got := defaultProject([]string{"platform-admins"}); got != "" {
		t.Errorf("defaultProject = %q, want none for an admin", got)
	}
}

// The admin's file becomes a context in the person's kubeconfig that runs
// airo, and keeps a namespace the person chose before.
func TestAdd(t *testing.T) {
	src := clientcmdapi.NewConfig()
	src.Clusters["airo"] = &clientcmdapi.Cluster{Server: "https://api.example:6443", CertificateAuthorityData: []byte("ca")}
	src.AuthInfos["airo"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
		APIVersion: execAPIVersion, Command: "kubectl",
		Args: []string{"oidc-login", "get-token", "--oidc-issuer-url=https://issuer.example/", "--oidc-client-id=abc"},
	}}
	src.Contexts["airo"] = &clientcmdapi.Context{Cluster: "airo", AuthInfo: "airo"}
	src.CurrentContext = "airo"
	file := filepath.Join(t.TempDir(), "airo.kubeconfig")
	if err := clientcmd.WriteToFile(*src, file); err != nil {
		t.Fatal(err)
	}

	cfg := clientcmdapi.NewConfig()
	cfg.Contexts["airo"] = &clientcmdapi.Context{Cluster: "old", AuthInfo: "old", Namespace: "mine"}
	name, o, err := add(cfg, file)
	if err != nil {
		t.Fatal(err)
	}
	if name != "airo" || o != (oidc{issuer: "https://issuer.example/", clientID: "abc"}) {
		t.Errorf("add = %q, %+v", name, o)
	}
	if got := cfg.Clusters["airo"]; got.Server != "https://api.example:6443" || got.LocationOfOrigin != "" {
		t.Errorf("cluster = %+v", got)
	}
	if got := cfg.AuthInfos["airo"].Exec; got.Command != "airo" || got.Args[0] != "credential" {
		t.Errorf("exec = %+v", got)
	}
	if got := cfg.Contexts["airo"]; got.Cluster != "airo" || got.Namespace != "mine" {
		t.Errorf("context = %+v", got)
	}
}

// issuer is an OIDC issuer that answers a refresh with a new ID token.
func issuer(t *testing.T, id string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"device_authorization_endpoint":"%s/device","token_endpoint":"%s/token"}`, srv.URL, srv.URL)
		case "/token":
			if r.FormValue("grant_type") != "refresh_token" || r.FormValue("refresh_token") != "old-refresh" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"access_token":"a","token_type":"Bearer","id_token":"%s","refresh_token":"new-refresh"}`, id)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A customer's first airo login -f signs in with the device flow and writes
// the tenant's default project into the context. -n replaces it, and a later
// airo login keeps the namespace the person chose.
func TestRunSetsACustomersDefaultProject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("PATH", "") // no browser opens

	id := jwt(time.Now().Add(time.Hour))
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"device_authorization_endpoint":"%s/device","token_endpoint":"%s/token"}`, idp.URL, idp.URL)
		case "/device":
			fmt.Fprintf(w, `{"device_code":"d","user_code":"ABCD","verification_uri":"%s/activate","interval":1}`, idp.URL)
		case "/token":
			fmt.Fprintf(w, `{"access_token":"a","token_type":"Bearer","id_token":"%s","refresh_token":"r"}`, id)
		}
	}))
	t.Cleanup(idp.Close)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+id {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"apiVersion":"authentication.k8s.io/v1","kind":"SelfSubjectReview",
			"status":{"userInfo":{"username":"auth0:me","groups":["customer:acme","system:authenticated"]}}}`)
	}))
	t.Cleanup(api.Close)

	// The file from the platform's admin.
	src := clientcmdapi.NewConfig()
	src.Clusters["airo"] = &clientcmdapi.Cluster{Server: api.URL}
	src.AuthInfos["airo"] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
		APIVersion: execAPIVersion, Command: "kubectl",
		Args: []string{"oidc-login", "get-token", "--oidc-issuer-url=" + idp.URL, "--oidc-client-id=abc"},
	}}
	src.Contexts["airo"] = &clientcmdapi.Context{Cluster: "airo", AuthInfo: "airo"}
	src.CurrentContext = "airo"
	file := filepath.Join(dir, "airo.kubeconfig")
	if err := clientcmd.WriteToFile(*src, file); err != nil {
		t.Fatal(err)
	}

	kubeconfig := filepath.Join(dir, "config")
	login := func(file, namespace string) string {
		t.Helper()
		flags := genericclioptions.NewConfigFlags(true)
		flags.KubeConfig = &kubeconfig
		flags.Namespace = &namespace
		var out bytes.Buffer
		if err := run(context.Background(), &out, &bytes.Buffer{}, flags, file); err != nil {
			t.Fatalf("login: %v", err)
		}
		cfg, err := clientcmd.LoadFromFile(kubeconfig)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.CurrentContext != "airo" || cfg.AuthInfos["airo"].Exec.Command != "airo" {
			t.Fatalf("kubeconfig = %+v", cfg)
		}
		return cfg.Contexts["airo"].Namespace
	}

	if got := login(file, ""); got != "acme-default" {
		t.Errorf("first login: namespace = %q, want acme-default", got)
	}
	if got := login("", "acme-web"); got != "acme-web" {
		t.Errorf("login -n: namespace = %q, want acme-web", got)
	}
	if got := login("", ""); got != "acme-web" {
		t.Errorf("login again: namespace = %q, want the chosen acme-web to stay", got)
	}
}

func TestTokenUsesTheCacheAndRefreshes(t *testing.T) {
	// os.UserCacheDir reads these.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)

	fresh := jwt(time.Now().Add(time.Hour))
	o := oidc{issuer: issuer(t, fresh).URL, clientID: "abc"}
	path, err := o.cachePath()
	if err != nil {
		t.Fatal(err)
	}

	// A valid cached token is returned without asking the issuer.
	valid := jwt(time.Now().Add(30 * time.Minute))
	if err := save(path, tokens{IDToken: valid, RefreshToken: "old-refresh"}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := o.token(context.Background(), &bytes.Buffer{}, false); err != nil || got != valid {
		t.Fatalf("token = %q, %v; want the cached one", got, err)
	}

	// An expired one is refreshed, and the new refresh token is kept.
	if err := save(path, tokens{IDToken: jwt(time.Now().Add(-time.Minute)), RefreshToken: "old-refresh"}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := o.token(context.Background(), &bytes.Buffer{}, false); err != nil || got != fresh {
		t.Fatalf("token = %q, %v; want the refreshed one", got, err)
	}
	if got := load(path); got.IDToken != fresh || got.RefreshToken != "new-refresh" {
		t.Errorf("cache = %+v", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode = %v, %v; want 0600", info.Mode(), err)
	}
}
