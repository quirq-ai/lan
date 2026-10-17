package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Local files (override the root with LAN_HOME):
//
//	~/.lan/config.json               server URL + Clerk token
//	~/.lan/networks/<id>/net.json    one per joined network
//	~/.lan/networks/<id>/tsnet/      tsnet node state (the node's identity)
func home() string {
	if h := os.Getenv("LAN_HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".lan")
}

type Config struct {
	Server       string    `json:"server"`
	Token        string    `json:"token"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
}

type NetConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Handle     string `json:"handle"`
	ControlURL string `json:"controlURL"`
	AuthKey    string `json:"authKey,omitempty"` // single-use, cleared after first `lan up`
	Owner      bool   `json:"owner"`
}

func (n *NetConfig) dir() string { return filepath.Join(home(), "networks", n.ID) }

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

func loadConfig() (*Config, error) {
	c := &Config{Server: os.Getenv("LAN_SERVER")}
	err := readJSONFile(filepath.Join(home(), "config.json"), c)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if s := os.Getenv("LAN_SERVER"); s != "" {
		c.Server = s
	}
	return c, nil
}

func (c *Config) save() error { return writeJSONFile(filepath.Join(home(), "config.json"), c) }

func (n *NetConfig) save() error { return writeJSONFile(filepath.Join(n.dir(), "net.json"), n) }

func listNetworks() []*NetConfig {
	dirs, _ := filepath.Glob(filepath.Join(home(), "networks", "*", "net.json"))
	var out []*NetConfig
	for _, d := range dirs {
		n := &NetConfig{}
		if readJSONFile(d, n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// pickNetwork resolves --net (id or name); empty works when only one is joined.
func pickNetwork(sel string) (*NetConfig, error) {
	nets := listNetworks()
	var names []string
	for _, n := range nets {
		if sel == "" && len(nets) == 1 || n.ID == sel || n.Name == sel {
			return n, nil
		}
		names = append(names, n.Name+" ("+n.ID+")")
	}
	if len(nets) == 0 {
		return nil, errors.New("not in any network yet: run `lan create <name>` or `lan join <code>`")
	}
	if sel == "" {
		return nil, fmt.Errorf("you are in several networks, pick one with --net: %s", strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("no network %q; yours: %s", sel, strings.Join(names, ", "))
}

// api calls lan-server with the saved Clerk token.
func api(method, path string, body, out any) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if c.Server == "" {
		return errors.New("no server configured: run `lan login --server <url>`")
	}
	tok, err := c.accessToken()
	if err != nil {
		return err
	}
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, strings.TrimRight(c.Server, "/")+path, r)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		var e struct{ Error string }
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			if res.StatusCode == 401 {
				return errors.New("not logged in (or session expired): run `lan login`")
			}
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, res.Status)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
