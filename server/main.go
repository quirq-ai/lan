// lan-server: owns networks, members and invites, and is the only thing that
// talks to Headscale. See ../DESIGN.md.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	inviteTTL  = 24 * time.Hour
	authKeyTTL = 5 * time.Minute
)

type server struct {
	store      *Store
	hs         *Headscale
	auth       *Auth
	controlURL string // public Headscale URL handed to clients
	clientID   string // Clerk OAuth client ID, served to the CLI for login
}

func main() {
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	store, err := OpenStore(env("LAN_STATE", "data/state.json"))
	if err != nil {
		log.Fatal(err)
	}
	httpc := &http.Client{Timeout: 15 * time.Second}
	s := &server{
		store:      store,
		hs:         &Headscale{URL: env("HEADSCALE_URL", "http://127.0.0.1:8080"), APIKey: os.Getenv("HEADSCALE_API_KEY"), HTTP: httpc},
		auth:       &Auth{Issuer: os.Getenv("CLERK_ISSUER"), DevMode: os.Getenv("LAN_DEV_AUTH") == "1", HTTP: httpc},
		controlURL: env("LAN_CONTROL_URL", "http://127.0.0.1:8080"),
		clientID:   os.Getenv("CLERK_OAUTH_CLIENT_ID"),
	}
	if s.hs.APIKey == "" {
		log.Fatal("HEADSCALE_API_KEY is required")
	}
	if s.auth.DevMode {
		log.Print("WARNING: LAN_DEV_AUTH=1 — any 'dev-<name>' token is accepted. Never use in production.")
	}
	// Push the policy once at boot so Headscale matches state.json.
	if err := s.hs.SetPolicy(BuildPolicy(&store.State)); err != nil {
		log.Printf("initial policy sync failed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /config", s.config)
	mux.HandleFunc("POST /networks", s.authed(s.createNetwork))
	mux.HandleFunc("POST /networks/{id}/invites", s.authed(s.createInvite))
	mux.HandleFunc("POST /networks/{id}/kick", s.authed(s.kick))
	mux.HandleFunc("POST /join", s.authed(s.join))

	addr := env("LAN_LISTEN", ":8443")
	log.Printf("lan-server listening on %s (headscale %s)", addr, s.hs.URL)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// --- handlers ---------------------------------------------------------------

type joinResponse struct {
	Network    Network `json:"network"`
	Handle     string  `json:"handle"`
	AuthKey    string  `json:"authKey"`
	ControlURL string  `json:"controlURL"`
	Owner      bool    `json:"owner"`
	Code       string  `json:"code,omitempty"` // first invite, on create only
}

func (s *server) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"clerkIssuer": s.auth.Issuer,
		"clientID":    s.clientID,
		"devAuth":     s.auth.DevMode,
	})
}

func (s *server) createNetwork(w http.ResponseWriter, r *http.Request, id Identity) {
	var req struct{ Name, Handle string }
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		httpError(w, 400, "name must be 1-64 characters")
		return
	}
	s.store.Lock()
	n := Network{ID: randomID(6), Name: req.Name, Owner: id.ID, Created: time.Now().UTC()}
	s.store.Networks = append(s.store.Networks, n)
	code := s.newInvite(n.ID)
	s.store.Unlock()

	res, status, err := s.admit(id, n.ID, req.Handle)
	if err != nil {
		httpError(w, status, err.Error())
		return
	}
	res.Code = code
	writeJSON(w, 200, res)
}

func (s *server) createInvite(w http.ResponseWriter, r *http.Request, id Identity) {
	s.store.Lock()
	defer s.store.Unlock()
	n := s.store.network(r.PathValue("id"))
	if n == nil || n.Owner != id.ID {
		httpError(w, 404, "network not found or you are not its owner")
		return
	}
	code := s.newInvite(n.ID)
	if err := s.store.Save(); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"code": code})
}

func (s *server) join(w http.ResponseWriter, r *http.Request, id Identity) {
	var req struct{ Code, Handle string }
	if !readJSON(w, r, &req) {
		return
	}
	hash := hashCode(req.Code)
	s.store.Lock()
	var netID string
	for i := range s.store.Invites {
		inv := &s.store.Invites[i]
		if inv.Hash == hash && !inv.Used && time.Now().Before(inv.Expires) {
			inv.Used = true
			netID = inv.Network
		}
	}
	s.store.Unlock()
	if netID == "" {
		httpError(w, 403, "invite code is invalid, used or expired")
		return
	}
	res, status, err := s.admit(id, netID, req.Handle)
	if err != nil {
		s.store.Lock() // give the invite back so the user can retry
		for i := range s.store.Invites {
			if s.store.Invites[i].Hash == hash {
				s.store.Invites[i].Used = false
			}
		}
		s.store.Unlock()
		httpError(w, status, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *server) kick(w http.ResponseWriter, r *http.Request, id Identity) {
	var req struct{ Handle string }
	if !readJSON(w, r, &req) {
		return
	}
	s.store.Lock()
	defer s.store.Unlock()
	n := s.store.network(r.PathValue("id"))
	if n == nil || n.Owner != id.ID {
		httpError(w, 404, "network not found or you are not its owner")
		return
	}
	m := s.store.memberByHandle(n.ID, req.Handle)
	if m == nil {
		httpError(w, 404, "no such member")
		return
	}
	if m.ClerkID == n.Owner {
		httpError(w, 400, "the owner cannot be kicked")
		return
	}
	if err := s.hs.DeleteUser(m.HSUserID, m.HSUser); err != nil {
		httpError(w, 502, err.Error())
		return
	}
	s.store.removeMember(n.ID, req.Handle)
	if err := s.hs.SetPolicy(BuildPolicy(&s.store.State)); err != nil {
		log.Printf("policy update after kick: %v", err)
	}
	if err := s.store.Save(); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"kicked": req.Handle})
}

// admit makes id a member of the network (or finds the existing membership)
// and mints a fresh single-use pre-auth key for its Headscale user.
func (s *server) admit(id Identity, netID, handle string) (joinResponse, int, error) {
	s.store.Lock()
	defer s.store.Unlock()
	n := s.store.network(netID)
	if n == nil {
		return joinResponse{}, 404, errors.New("network not found")
	}
	m := s.store.memberByClerk(netID, id.ID)
	if m == nil {
		h := sanitizeHandle(cmpOr(handle, id.Username))
		if s.store.memberByHandle(netID, h) != nil {
			return joinResponse{}, 409, errors.New("handle '" + h + "' is taken in this network; pick another with --as")
		}
		hsName := h + "-" + netID
		uid, err := s.hs.EnsureUser(hsName)
		if err != nil {
			return joinResponse{}, 502, err
		}
		s.store.Members = append(s.store.Members, Member{Network: netID, ClerkID: id.ID, Handle: h, HSUser: hsName, HSUserID: uid})
		if err := s.hs.SetPolicy(BuildPolicy(&s.store.State)); err != nil {
			s.store.removeMember(netID, h)
			return joinResponse{}, 502, err
		}
		m = s.store.memberByHandle(netID, h)
	}
	if err := s.store.Save(); err != nil {
		return joinResponse{}, 500, err
	}
	key, err := s.hs.PreAuthKey(m.HSUserID, authKeyTTL)
	if err != nil {
		return joinResponse{}, 502, err
	}
	return joinResponse{Network: *n, Handle: m.Handle, AuthKey: key, ControlURL: s.controlURL, Owner: n.Owner == id.ID}, 200, nil
}

// --- helpers ----------------------------------------------------------------

// newInvite must be called with the store locked; the caller saves.
func (s *server) newInvite(netID string) string {
	b := make([]byte, 16) // 128 bits
	rand.Read(b)
	code := "lan_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
	s.store.Invites = append(s.store.Invites, Invite{Hash: hashCode(code), Network: netID, Expires: time.Now().Add(inviteTTL)})
	return code
}

func hashCode(code string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(h[:])
}

func randomID(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

var handleJunk = regexp.MustCompile(`[^a-z0-9-]+`)

func sanitizeHandle(h string) string {
	h = strings.Trim(handleJunk.ReplaceAllString(strings.ToLower(h), "-"), "-")
	if len(h) > 24 {
		h = strings.TrimRight(h[:24], "-")
	}
	if h == "" {
		h = "user"
	}
	return h
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *server) authed(h func(http.ResponseWriter, *http.Request, Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := s.auth.Identify(r)
		if err != nil {
			httpError(w, 401, err.Error())
			return
		}
		h(w, r, id)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		httpError(w, 400, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
