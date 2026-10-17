package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Fixed loopback redirect so it can be registered on the Clerk OAuth app.
const redirectAddr = "127.0.0.1:53682"

type serverConfig struct {
	ClerkIssuer string `json:"clerkIssuer"`
	ClientID    string `json:"clientID"`
	DevAuth     bool   `json:"devAuth"`
}

func fetchServerConfig(server string) (*serverConfig, error) {
	res, err := (&http.Client{Timeout: 10 * time.Second}).Get(strings.TrimRight(server, "/") + "/config")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	sc := &serverConfig{}
	return sc, json.NewDecoder(res.Body).Decode(sc)
}

func oauthConfig(sc *serverConfig) *oauth2.Config {
	iss := strings.TrimRight(sc.ClerkIssuer, "/")
	return &oauth2.Config{
		ClientID:    sc.ClientID,
		Endpoint:    oauth2.Endpoint{AuthURL: iss + "/oauth/authorize", TokenURL: iss + "/oauth/token", AuthStyle: oauth2.AuthStyleInParams},
		RedirectURL: "http://" + redirectAddr + "/callback",
		Scopes:      []string{"profile", "email"},
	}
}

// login runs Clerk OAuth (authorization code + PKCE, loopback redirect), or
// stores a token directly with --token (dev mode / CI).
func login(server, token string) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if server != "" {
		c.Server = server
	}
	if c.Server == "" {
		return errors.New("pass --server <lan-server url> (or set LAN_SERVER)")
	}
	if token != "" {
		c.Token, c.RefreshToken, c.Expiry = token, "", time.Time{}
		return c.save()
	}

	sc, err := fetchServerConfig(c.Server)
	if err != nil {
		return fmt.Errorf("reach %s: %w", c.Server, err)
	}
	if sc.ClientID == "" {
		if sc.DevAuth {
			return errors.New("server is in dev-auth mode: use `lan login --token dev-<yourname>`")
		}
		return errors.New("server has no Clerk OAuth client configured")
	}
	oc := oauthConfig(sc)
	verifier := oauth2.GenerateVerifier()
	state := oauth2.GenerateVerifier()

	ln, err := net.Listen("tcp", redirectAddr)
	if err != nil {
		return fmt.Errorf("listen on %s for the login callback: %w", redirectAddr, err)
	}
	codeCh, errCh := make(chan string, 1), make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state || q.Get("code") == "" {
			http.Error(w, "login failed", 400)
			errCh <- fmt.Errorf("login failed: %s", cmpOr(q.Get("error_description"), q.Get("error"), "bad callback"))
			return
		}
		fmt.Fprintln(w, "Logged in to lan. You can close this tab.")
		codeCh <- q.Get("code")
	})}
	go srv.Serve(ln)
	defer srv.Close()

	url := oc.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	fmt.Println("Opening your browser to log in. If it doesn't open, visit:\n  " + url)
	openBrowser(url)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Minute):
		return errors.New("timed out waiting for login")
	}
	tok, err := oc.Exchange(context.Background(), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return err
	}
	c.Token, c.RefreshToken, c.Expiry = tok.AccessToken, tok.RefreshToken, tok.Expiry
	return c.save()
}

// accessToken returns a valid token, refreshing it with Clerk when expired.
func (c *Config) accessToken() (string, error) {
	if c.Token == "" {
		return "", errors.New("not logged in: run `lan login`")
	}
	if c.Expiry.IsZero() || time.Until(c.Expiry) > 30*time.Second || c.RefreshToken == "" {
		return c.Token, nil
	}
	sc, err := fetchServerConfig(c.Server)
	if err != nil {
		return "", err
	}
	tok, err := oauthConfig(sc).TokenSource(context.Background(), &oauth2.Token{RefreshToken: c.RefreshToken}).Token()
	if err != nil {
		return "", fmt.Errorf("session expired, run `lan login`: %w", err)
	}
	c.Token, c.Expiry = tok.AccessToken, tok.Expiry
	if tok.RefreshToken != "" {
		c.RefreshToken = tok.RefreshToken
	}
	return c.Token, c.save()
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
