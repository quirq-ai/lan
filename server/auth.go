package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Identity struct {
	ID       string // Clerk user ID
	Username string // suggested handle
}

// Auth resolves a bearer token to a Clerk user by calling Clerk's OAuth
// userinfo endpoint: https://clerk.com/docs/advanced-usage/clerk-idp
type Auth struct {
	Issuer  string // Clerk Frontend API URL, e.g. https://clerk.example.com
	DevMode bool   // accept "dev-<user>" tokens; never enable in production
	HTTP    *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	id  Identity
	exp time.Time
}

var errUnauthorized = errors.New("unauthorized")

func (a *Auth) Identify(r *http.Request) (Identity, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return Identity{}, errUnauthorized
	}
	if a.DevMode {
		if user, ok := strings.CutPrefix(tok, "dev-"); ok && user != "" {
			return Identity{ID: "dev_" + user, Username: user}, nil
		}
	}
	if a.Issuer == "" {
		return Identity{}, errUnauthorized
	}

	a.mu.Lock()
	if c, ok := a.cache[tok]; ok && time.Now().Before(c.exp) {
		a.mu.Unlock()
		return c.id, nil
	}
	a.mu.Unlock()

	req, _ := http.NewRequest("GET", strings.TrimRight(a.Issuer, "/")+"/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := a.HTTP.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("clerk userinfo: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Identity{}, errUnauthorized
	}
	var info struct {
		Sub      string `json:"sub"`
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil || info.Sub == "" {
		return Identity{}, errUnauthorized
	}
	id := Identity{ID: info.Sub, Username: info.Username}
	if id.Username == "" {
		id.Username, _, _ = strings.Cut(info.Email, "@")
	}

	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[string]cached{}
	}
	a.cache[tok] = cached{id, time.Now().Add(time.Minute)}
	a.mu.Unlock()
	return id, nil
}
