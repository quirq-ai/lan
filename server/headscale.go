package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Headscale is a minimal client for the Headscale REST API (v0.29).
// Docs: https://headscale.net/stable/ref/api/ — full schema at <server>/swagger.
type Headscale struct {
	URL    string // e.g. http://headscale:8080
	APIKey string
	HTTP   *http.Client
}

func (h *Headscale) do(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(h.URL, "/")+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := h.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("headscale %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("headscale %s %s: %s: %s", method, path, res.Status, bytes.TrimSpace(b))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

type hsUser struct {
	ID   json.Number `json:"id"` // uint64, encoded as a string
	Name string      `json:"name"`
}

type hsNode struct {
	ID   json.Number `json:"id"`
	User hsUser      `json:"user"`
}

// EnsureUser returns the ID of the named user, creating it if needed.
func (h *Headscale) EnsureUser(name string) (string, error) {
	var list struct {
		Users []hsUser `json:"users"`
	}
	if err := h.do("GET", "/api/v1/user?name="+url.QueryEscape(name), nil, &list); err == nil {
		for _, u := range list.Users {
			if u.Name == name {
				return u.ID.String(), nil
			}
		}
	}
	var created struct {
		User hsUser `json:"user"`
	}
	if err := h.do("POST", "/api/v1/user", map[string]any{"name": name}, &created); err != nil {
		return "", err
	}
	return created.User.ID.String(), nil
}

// PreAuthKey mints a single-use key for the user that expires after ttl.
func (h *Headscale) PreAuthKey(userID string, ttl time.Duration) (string, error) {
	var out struct {
		PreAuthKey struct {
			Key string `json:"key"`
		} `json:"preAuthKey"`
	}
	err := h.do("POST", "/api/v1/preauthkey", map[string]any{
		"user":       userID,
		"reusable":   false,
		"ephemeral":  false,
		"expiration": time.Now().Add(ttl).UTC().Format(time.RFC3339),
	}, &out)
	return out.PreAuthKey.Key, err
}

// DeleteUser removes every node owned by the user, then the user itself.
func (h *Headscale) DeleteUser(userID, name string) error {
	var list struct {
		Nodes []hsNode `json:"nodes"`
	}
	if err := h.do("GET", "/api/v1/node?user="+url.QueryEscape(name), nil, &list); err != nil {
		return err
	}
	for _, n := range list.Nodes {
		if n.User.Name != name { // older servers ignore the filter
			continue
		}
		if err := h.do("DELETE", "/api/v1/node/"+n.ID.String(), nil, nil); err != nil {
			return err
		}
	}
	return h.do("DELETE", "/api/v1/user/"+userID, nil, nil)
}

// SetPolicy replaces the ACL policy. Requires `policy.mode: database`.
func (h *Headscale) SetPolicy(policy string) error {
	return h.do("PUT", "/api/v1/policy", map[string]any{"policy": policy}, nil)
}

// BuildPolicy gives each network one group whose members can reach only each
// other. Everything else is denied.
func BuildPolicy(st *State) string {
	type acl struct {
		Action string   `json:"action"`
		Src    []string `json:"src"`
		Dst    []string `json:"dst"`
	}
	groups := map[string][]string{}
	for _, m := range st.Members {
		g := "group:" + m.Network
		groups[g] = append(groups[g], m.HSUser+"@")
	}
	acls := []acl{}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	slices.Sort(names)
	for _, g := range names {
		acls = append(acls, acl{Action: "accept", Src: []string{g}, Dst: []string{g + ":*"}})
	}
	b, _ := json.MarshalIndent(map[string]any{"groups": groups, "acls": acls}, "", "  ")
	return string(b)
}
