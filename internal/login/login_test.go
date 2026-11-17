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
	"strings"
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

func kubeloginUser() *clientcmdapi.AuthInfo {
	return &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
		APIVersion: execAPIVersion, Command: "kubectl",
		Args: []string{"oidc-login", "get-token", "--oidc-issuer-url=https://issuer.example/", "--oidc-client-id=abc"},
	}}
}

func TestPrepare(t *testing.T) {
	all := platform{server: "https://api.example:6443", ca: []byte("ca"), issuer: "https://issuer.example/", clientID: "abc"}

	t.Run("an empty kubeconfig needs the platform's values", func(t *testing.T) {
		_, _, err := prepare(clientcmdapi.NewConfig(), "", platform{server: "https://api.example:6443"})
		if err == nil || !strings.Contains(err.Error(), "--issuer, --client-id") || strings.Contains(err.Error(), "--server") {
			t.Errorf("err = %v, want it to name the missing flags only", err)
		}
	})

	t.Run("the first sign-in makes the context airo and leaves a local cluster alone", func(t *testing.T) {
		cfg := clientcmdapi.NewConfig()
		cfg.Clusters["local"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:6443"}
		cfg.AuthInfos["local"] = &clientcmdapi.AuthInfo{Token: "x"}
		cfg.Contexts["local"] = &clientcmdapi.Context{Cluster: "local", AuthInfo: "local"}
		cfg.CurrentContext = "local"

		name, o, err := prepare(cfg, "", all)
		if err != nil {
			t.Fatal(err)
		}
		if want := (oidc{issuer: "https://issuer.example/", clientID: "abc", session: "airo"}); name != "airo" || o != want {
			t.Errorf("prepare = %q, %+v", name, o)
		}
		if got := cfg.Clusters["airo"]; got.Server != all.server || string(got.CertificateAuthorityData) != "ca" {
			t.Errorf("cluster = %+v", got)
		}
		if got := cfg.AuthInfos["airo"].Exec; got.Command != "airo" || got.Args[len(got.Args)-1] != "--session=airo" {
			t.Errorf("exec = %+v", got)
		}
		if cfg.Clusters["local"].Server != "https://127.0.0.1:6443" || cfg.AuthInfos["local"].Token != "x" {
			t.Errorf("the local cluster's entries changed")
		}

		// It is someone else's context, also when named.
		if _, _, err := prepare(cfg, "local", all); err == nil {
			t.Errorf("prepare took over the context local")
		}
	})

	t.Run("a new context is a second sign-in to the current platform", func(t *testing.T) {
		cfg := clientcmdapi.NewConfig()
		cfg.Clusters["airo"] = &clientcmdapi.Cluster{Server: "https://api.example:6443"}
		cfg.AuthInfos["airo"] = kubeloginUser()
		cfg.Contexts["airo"] = &clientcmdapi.Context{Cluster: "airo", AuthInfo: "airo", Namespace: "mine"}
		cfg.CurrentContext = "airo"

		name, o, err := prepare(cfg, "customer", platform{})
		if err != nil {
			t.Fatal(err)
		}
		if want := (oidc{issuer: "https://issuer.example/", clientID: "abc", session: "customer"}); name != "customer" || o != want {
			t.Errorf("prepare = %q, %+v", name, o)
		}
		if got := cfg.Contexts["customer"]; got.Cluster != "airo" || got.AuthInfo != "customer" || got.Namespace != "" {
			t.Errorf("context = %+v", got)
		}
		if got := cfg.AuthInfos["airo"].Exec.Command; got != "kubectl" {
			t.Errorf("the other context's user now runs %q", got)
		}

		// Signing in again takes the entry over from kubelogin and keeps
		// the namespace. A new issuer replaces the old one.
		name, o, err = prepare(cfg, "airo", platform{issuer: "https://new.example/"})
		if err != nil {
			t.Fatal(err)
		}
		if want := (oidc{issuer: "https://new.example/", clientID: "abc", session: "airo"}); name != "airo" || o != want {
			t.Errorf("prepare = %q, %+v", name, o)
		}
		if got := cfg.AuthInfos["airo"].Exec.Command; got != "airo" {
			t.Errorf("exec command = %q", got)
		}
		if got := cfg.Contexts["airo"].Namespace; got != "mine" {
			t.Errorf("namespace = %q", got)
		}
	})

	t.Run("a new context with its own address gets its own cluster", func(t *testing.T) {
		cfg := clientcmdapi.NewConfig()
		cfg.Clusters["airo"] = &clientcmdapi.Cluster{Server: "https://api.example:6443", CertificateAuthorityData: []byte("ca")}
		cfg.AuthInfos["airo"] = kubeloginUser()
		cfg.Contexts["airo"] = &clientcmdapi.Context{Cluster: "airo", AuthInfo: "airo"}
		cfg.CurrentContext = "airo"

		if _, _, err := prepare(cfg, "staging", platform{server: "https://staging.example:6443"}); err != nil {
			t.Fatal(err)
		}
		if got := cfg.Clusters["staging"]; got == nil || got.Server != "https://staging.example:6443" || string(got.CertificateAuthorityData) != "ca" {
			t.Errorf("cluster = %+v", got)
		}
		if got := cfg.Clusters["airo"].Server; got != "https://api.example:6443" {
			t.Errorf("the first cluster's server = %q", got)
		}
	})
}

// The first airo login takes the platform from the flags. A second context
// signs in someone else next to it, with their own tokens, and a customer
// starts in the tenant's default project. -n replaces it, and a later airo
// login keeps the namespace the person chose.
func TestRunSignsInTwoPeople(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("PATH", "") // no browser opens

	// Each sign-in gets the next token: first the admin's, then the
	// customer's.
	admin, customer := jwt(time.Now().Add(time.Hour)), jwt(time.Now().Add(2*time.Hour))
	signIns := 0
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"device_authorization_endpoint":"%s/device","token_endpoint":"%s/token"}`, idp.URL, idp.URL)
		case "/device":
			fmt.Fprintf(w, `{"device_code":"d","user_code":"ABCD","verification_uri":"%s/activate","interval":1}`, idp.URL)
		case "/token":
			id := customer
			if signIns == 0 {
				id = admin
			}
			signIns++
			fmt.Fprintf(w, `{"access_token":"a","token_type":"Bearer","id_token":"%s","refresh_token":"r"}`, id)
		}
	}))
	t.Cleanup(idp.Close)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := ""
		switch r.Header.Get("Authorization") {
		case "Bearer " + admin:
			user = `{"username":"auth0:admin","groups":["platform-admins"]}`
		case "Bearer " + customer:
			user = `{"username":"auth0:customer","groups":["customer:acme","system:authenticated"]}`
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"apiVersion":"authentication.k8s.io/v1","kind":"SelfSubjectReview","status":{"userInfo":%s}}`, user)
	}))
	t.Cleanup(api.Close)

	kubeconfig := filepath.Join(dir, "config")
	login := func(name, namespace string, p platform) (string, *clientcmdapi.Config) {
		t.Helper()
		flags := genericclioptions.NewConfigFlags(true)
		flags.KubeConfig = &kubeconfig
		flags.Context = &name
		flags.Namespace = &namespace
		var out bytes.Buffer
		if err := run(context.Background(), &out, &bytes.Buffer{}, flags, p); err != nil {
			t.Fatalf("login: %v", err)
		}
		cfg, err := clientcmd.LoadFromFile(kubeconfig)
		if err != nil {
			t.Fatal(err)
		}
		return out.String(), cfg
	}

	// No kubeconfig yet.
	out, cfg := login("", "", platform{server: api.URL, issuer: idp.URL, clientID: "abc"})
	if !strings.Contains(out, "Signed in as auth0:admin") || cfg.CurrentContext != "airo" || cfg.Contexts["airo"].Namespace != "" {
		t.Errorf("first login: %s%+v", out, cfg.Contexts["airo"])
	}

	out, cfg = login("customer", "", platform{})
	if !strings.Contains(out, "Signed in as auth0:customer") || cfg.CurrentContext != "customer" {
		t.Errorf("second login: %scurrent context %q", out, cfg.CurrentContext)
	}
	if got := cfg.Contexts["customer"]; got.Cluster != "airo" || got.Namespace != "acme-default" {
		t.Errorf("second login: context = %+v, want the first one's cluster and the namespace acme-default", got)
	}

	// Each context's exec plugin reads its own tokens.
	for name, want := range map[string]string{"airo": admin, "customer": customer} {
		o, err := oidcFrom(cfg.AuthInfos[name])
		if err != nil {
			t.Fatal(err)
		}
		if got, _, err := o.token(context.Background(), &bytes.Buffer{}, false); err != nil || got != want {
			t.Errorf("context %s: the cached token is not its own: %v", name, err)
		}
	}

	if _, cfg = login("customer", "acme-web", platform{}); cfg.Contexts["customer"].Namespace != "acme-web" {
		t.Errorf("login -n: namespace = %q, want acme-web", cfg.Contexts["customer"].Namespace)
	}
	if _, cfg = login("", "", platform{}); cfg.CurrentContext != "customer" || cfg.Contexts["customer"].Namespace != "acme-web" {
		t.Errorf("login again: context %q, namespace %q; want the chosen acme-web to stay", cfg.CurrentContext, cfg.Contexts["customer"].Namespace)
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
