package login

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// oidc is the platform's sign-in: the issuer the API server trusts and the
// public client people sign in through. session names one sign-in there, so
// two contexts can hold two people's tokens.
type oidc struct {
	issuer   string
	clientID string
	session  string
}

// tokens is what the cache file holds.
type tokens struct {
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// expirySkew is how long before its expiry a token counts as expired, so a
// token handed to kubectl is still valid when the API server reads it.
const expirySkew = time.Minute

// token returns an ID token and its expiry: the cached one, a refreshed one,
// or a new one after the person signs in.
func (o oidc) token(ctx context.Context, prompt io.Writer) (string, time.Time, error) {
	path, err := o.cachePath()
	if err != nil {
		return "", time.Time{}, err
	}
	cached := load(path)
	if exp, err := expiry(cached.IDToken); err == nil && time.Until(exp) > expirySkew {
		return cached.IDToken, exp, nil
	}
	token, exp, err := o.authenticate(ctx, prompt, cached.RefreshToken)
	if err != nil {
		return "", time.Time{}, err
	}
	return token.IDToken, exp, save(path, token)
}

// authenticate leaves the sign-in cache untouched until the caller has
// verified the identity and the tenant selected for it.
func (o oidc) authenticate(ctx context.Context, prompt io.Writer, refresh string) (tokens, time.Time, error) {
	cfg, err := o.config(ctx)
	if err != nil {
		return tokens{}, time.Time{}, err
	}

	var tok *oauth2.Token
	if refresh != "" {
		// A refused refresh token, such as one older than its lifetime,
		// falls through to signing in.
		tok, _ = cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refresh}).Token()
	}
	if tok == nil {
		if tok, err = signIn(ctx, cfg, prompt); err != nil {
			return tokens{}, time.Time{}, err
		}
	}
	id, _ := tok.Extra("id_token").(string)
	exp, err := expiry(id)
	if err != nil {
		return tokens{}, time.Time{}, fmt.Errorf("%s gave no usable ID token: %w", o.issuer, err)
	}
	return tokens{IDToken: id, RefreshToken: tok.RefreshToken}, exp, nil
}

// signIn runs the device flow: the person confirms a code in the browser, so
// it also works over SSH and in a container.
func signIn(ctx context.Context, cfg *oauth2.Config, prompt io.Writer) (*oauth2.Token, error) {
	da, err := cfg.DeviceAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("start the sign-in: %w", err)
	}
	url := da.VerificationURIComplete
	if url == "" {
		url = da.VerificationURI
	}
	fmt.Fprintf(prompt, "Open %s\nand confirm the code %s\n", url, da.UserCode)
	openBrowser(url)
	tok, err := cfg.DeviceAccessToken(ctx, da)
	if err != nil {
		return nil, fmt.Errorf("sign in: %w", err)
	}
	return tok, nil
}

// config reads the issuer's endpoints from its discovery document.
func (o oidc) config(ctx context.Context) (*oauth2.Config, error) {
	url := strings.TrimSuffix(o.issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	var doc struct {
		DeviceAuthURL string `json:"device_authorization_endpoint"`
		TokenURL      string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if doc.DeviceAuthURL == "" || doc.TokenURL == "" {
		return nil, fmt.Errorf("%s does not support signing in with a device code", o.issuer)
	}
	return &oauth2.Config{
		ClientID: o.clientID,
		Endpoint: oauth2.Endpoint{
			DeviceAuthURL: doc.DeviceAuthURL,
			TokenURL:      doc.TokenURL,
			AuthStyle:     oauth2.AuthStyleInParams,
		},
		// offline_access asks for the refresh token.
		Scopes: []string{"openid", "offline_access"},
	}, nil
}

// cachePath is the file with this sign-in's tokens, in the user's cache
// directory. The kubeconfig holds no credentials.
func (o oidc) cachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	key := o.issuer + "\n" + o.clientID
	if o.session != "" {
		// Without a session the path is the one from before airo had
		// sessions, so a kubeconfig entry from then still finds its tokens.
		key += "\n" + o.session
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, "airo", "tokens-"+hex.EncodeToString(sum[:8])+".json"), nil
}

func load(path string) tokens {
	var t tokens
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &t)
	}
	return t
}

func save(path string, t tokens) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// expiry reads the exp claim of a JWT. It does not check the signature: the
// API server does.
func expiry(jwt string) (time.Time, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}
	if claims.Exp == 0 {
		return time.Time{}, errors.New("the token has no exp claim")
	}
	return time.Unix(claims.Exp, 0), nil
}

// openBrowser is a convenience: the URL is printed too.
func openBrowser(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	_ = exec.Command(name, url).Start()
}
