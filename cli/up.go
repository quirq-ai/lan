package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

const manifestPort = 7000

type Share struct {
	Name string `json:"name"`
	Type string `json:"type"` // "port" | "dir"
	Port int    `json:"port"` // tailnet port the share listens on

	target string // local port or directory
}

type Connect struct{ Peer, Share, Local string }

type node struct {
	net    *NetConfig
	s      *tsnet.Server
	lc     *local.Client
	shares []Share

	mu    sync.Mutex
	cache map[string]cachedManifest // peer handle → manifest
}

type cachedManifest struct {
	ip     netip.Addr
	shares []Share
	at     time.Time
}

func up(nc *NetConfig, ports, dirs, connects []string, verbose bool) error {
	n := &node{net: nc, cache: map[string]cachedManifest{}}
	next := manifestPort + 1
	for _, kv := range ports {
		name, target, ok := strings.Cut(kv, "=")
		if _, err := strconv.Atoi(target); !ok || err != nil || name == "" {
			return fmt.Errorf("--port wants name=localport, got %q", kv)
		}
		n.shares = append(n.shares, Share{Name: name, Type: "port", Port: next, target: target})
		next++
	}
	for _, kv := range dirs {
		name, target, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return fmt.Errorf("--dir wants name=path, got %q", kv)
		}
		if st, err := os.Stat(target); err != nil || !st.IsDir() {
			return fmt.Errorf("--dir %s: %q is not a directory", name, target)
		}
		n.shares = append(n.shares, Share{Name: name, Type: "dir", Port: next, target: target})
		next++
	}
	var conns []Connect
	for _, c := range connects {
		ps, local, ok := strings.Cut(c, "=")
		peer, share, ok2 := strings.Cut(ps, "/")
		if _, err := strconv.Atoi(local); !ok || !ok2 || err != nil {
			return fmt.Errorf("--connect wants peer/share=localport, got %q", c)
		}
		conns = append(conns, Connect{peer, share, local})
	}

	n.s = &tsnet.Server{
		Dir:        filepath.Join(nc.dir(), "tsnet"),
		Hostname:   nc.Handle,
		ControlURL: nc.ControlURL,
		AuthKey:    nc.AuthKey,
		Logf:       func(string, ...any) {},
		UserLogf:   func(string, ...any) {},
	}
	if verbose {
		n.s.Logf = log.Printf
	}
	defer n.s.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	upCtx, upCancel := context.WithTimeout(ctx, 60*time.Second)
	st, err := n.s.Up(upCtx)
	upCancel()
	if err != nil {
		return fmt.Errorf("could not join the network (if you were removed, ask for a new invite and `lan join` again): %w", err)
	}
	if nc.AuthKey != "" { // single-use key is spent now
		nc.AuthKey = ""
		nc.save()
	}
	if n.lc, err = n.s.LocalClient(); err != nil {
		return err
	}
	ip := ""
	if len(st.TailscaleIPs) > 0 {
		ip = st.TailscaleIPs[0].String()
	}
	fmt.Printf("network %s · you are %s (%s)\n", nc.Name, nc.Handle, ip)

	if err := n.serveManifest(); err != nil {
		return err
	}
	for _, sh := range n.shares {
		if err := n.serveShare(sh); err != nil {
			return err
		}
	}
	if len(n.shares) > 0 {
		var parts []string
		for _, sh := range n.shares {
			arrow := ":" + sh.target
			if sh.Type == "dir" {
				arrow = sh.target + " (read-only)"
			}
			parts = append(parts, sh.Name+" → "+arrow)
		}
		fmt.Println("shares: " + strings.Join(parts, "   "))
	}
	for _, c := range conns {
		if err := n.connect(ctx, c); err != nil {
			return err
		}
		fmt.Printf("connect: %s/%s → localhost:%s\n", c.Peer, c.Share, c.Local)
	}

	go n.roster(ctx)
	<-ctx.Done()
	fmt.Println("\nleaving.")
	return nil
}

// allowed reports whether the caller is a member of this node's network. The
// Headscale ACL already enforces this; this check is a second line of defense.
func (n *node) allowed(ctx context.Context, remoteAddr string) bool {
	who, err := n.lc.WhoIs(ctx, remoteAddr)
	if err != nil || who.UserProfile == nil {
		return false
	}
	user, _, _ := strings.Cut(who.UserProfile.LoginName, "@")
	return strings.HasSuffix(user, "-"+n.net.ID)
}

func (n *node) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !n.allowed(r.Context(), r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (n *node) serveManifest() error {
	ln, err := n.s.Listen("tcp", fmt.Sprintf(":%d", manifestPort))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /shares", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(n.shares)
	})
	go http.Serve(ln, n.guard(mux))
	return nil
}

func (n *node) serveShare(sh Share) error {
	ln, err := n.s.Listen("tcp", fmt.Sprintf(":%d", sh.Port))
	if err != nil {
		return err
	}
	switch sh.Type {
	case "dir":
		root, err := os.OpenRoot(sh.target) // blocks ../ and symlink escapes
		if err != nil {
			return err
		}
		files := http.FileServer(http.FS(notFoundFS{root.FS()}))
		go http.Serve(ln, n.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "read-only share", http.StatusMethodNotAllowed)
				return
			}
			files.ServeHTTP(w, r)
		})))
	case "port":
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					if !n.allowed(context.Background(), c.RemoteAddr().String()) {
						return
					}
					dst, err := net.DialTimeout("tcp", "127.0.0.1:"+sh.target, 5*time.Second)
					if err != nil {
						return
					}
					pipe(c, dst)
				}()
			}
		}()
	}
	return nil
}

// notFoundFS turns every open error (including os.Root escape errors) into
// fs.ErrNotExist so the file server answers 404 rather than 500.
type notFoundFS struct{ fs.FS }

func (f notFoundFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, fs.ErrNotExist
	}
	return file, nil
}

// connect listens on localhost and forwards each connection to a peer's share.
func (n *node) connect(ctx context.Context, c Connect) error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+c.Local)
	if err != nil {
		return fmt.Errorf("--connect %s/%s: %w", c.Peer, c.Share, err)
	}
	go func() { <-ctx.Done(); ln.Close() }()
	if _, _, err := n.resolve(ctx, c.Peer, c.Share); err != nil {
		fmt.Printf("warning: %v (will retry on each connection)\n", err)
	}
	go func() {
		for {
			local, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer local.Close()
				ip, port, err := n.resolve(ctx, c.Peer, c.Share)
				if err != nil {
					fmt.Fprintf(os.Stderr, "connect %s/%s: %v\n", c.Peer, c.Share, err)
					return
				}
				dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				remote, err := n.s.Dial(dctx, "tcp", netip.AddrPortFrom(ip, uint16(port)).String())
				cancel()
				if err != nil {
					fmt.Fprintf(os.Stderr, "connect %s/%s: %v\n", c.Peer, c.Share, err)
					return
				}
				pipe(local, remote)
			}()
		}
	}()
	return nil
}

// resolve finds a peer by handle among visible peers (only same-network peers
// are visible) and looks the share up in its manifest. Cached for 30s.
func (n *node) resolve(ctx context.Context, peer, share string) (netip.Addr, int, error) {
	n.mu.Lock()
	m, ok := n.cache[peer]
	n.mu.Unlock()
	if !ok || time.Since(m.at) > 30*time.Second {
		st, err := n.lc.Status(ctx)
		if err != nil {
			return netip.Addr{}, 0, err
		}
		var ip netip.Addr
		for _, p := range st.Peer {
			if p.HostName == peer && len(p.TailscaleIPs) > 0 {
				ip = p.TailscaleIPs[0]
			}
		}
		if !ip.IsValid() {
			return netip.Addr{}, 0, fmt.Errorf("no peer %q in this network", peer)
		}
		hc := n.s.HTTPClient()
		hc.Timeout = 10 * time.Second
		res, err := hc.Get(fmt.Sprintf("http://%s/shares", netip.AddrPortFrom(ip, manifestPort)))
		if err != nil {
			return netip.Addr{}, 0, fmt.Errorf("%s is not reachable (is `lan up` running there?): %w", peer, err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return netip.Addr{}, 0, fmt.Errorf("%s refused: %s", peer, res.Status)
		}
		m = cachedManifest{ip: ip, at: time.Now()}
		if err := json.NewDecoder(res.Body).Decode(&m.shares); err != nil {
			return netip.Addr{}, 0, err
		}
		n.mu.Lock()
		n.cache[peer] = m
		n.mu.Unlock()
	}
	for _, sh := range m.shares {
		if sh.Name == share {
			return m.ip, sh.Port, nil
		}
	}
	var names []string
	for _, sh := range m.shares {
		names = append(names, sh.Name)
	}
	return netip.Addr{}, 0, fmt.Errorf("%s has no share %q (has: %s)", peer, share, strings.Join(names, ", "))
}

// roster prints the peer list whenever it changes.
func (n *node) roster(ctx context.Context) {
	last := ""
	for {
		if st, err := n.lc.Status(ctx); err == nil {
			var lines []string
			for _, p := range st.Peer {
				state := "offline"
				if p.Online {
					state = "online  relay(" + cmpOr(p.Relay, "derp") + ")"
					if p.CurAddr != "" {
						state = "online  direct"
					}
				}
				lines = append(lines, fmt.Sprintf("  %-12s %s", p.HostName, state))
			}
			slices.Sort(lines)
			out := strings.Join(lines, "\n")
			if len(lines) == 0 {
				out = "  (no other members yet)"
			}
			if out != last {
				fmt.Println("peers:\n" + out)
				last = out
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}
