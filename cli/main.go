// lan: private developer networks. Share a local port or folder with your
// teammates peer-to-peer. See ../DESIGN.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const usage = `lan — private developer networks

  lan login [--server URL] [--token T]   log in with Clerk (--token for dev/CI)
  lan create <name> [--as handle]        create a network → prints an invite code
  lan invite [--net n]                   new invite code (24h, single use; owner)
  lan join <code> [--as handle]          join a network
  lan networks                           list networks you're in
  lan up [--net n] [--port name=localport]... [--dir name=path]...
         [--connect peer/share=localport]... [-v]
                                         go online, share, and connect (Ctrl-C to leave)
  lan kick <handle> [--net n]            remove a member (owner)

env: LAN_SERVER (server URL), LAN_HOME (state dir, default ~/.lan)
`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(cmd string, args []string) error {
	fs := flag.NewFlagSet("lan "+cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	netSel := fs.String("net", "", "network id or name")
	as := fs.String("as", "", "your handle in the network")

	switch cmd {
	case "login":
		server := fs.String("server", "", "lan-server URL")
		token := fs.String("token", "", "use this token instead of browser login")
		parse(fs, args, 0)
		if err := login(*server, *token); err != nil {
			return err
		}
		fmt.Println("logged in.")
		return nil

	case "create":
		pos := parse(fs, args, 1)
		var res joinResponse
		if err := api("POST", "/networks", map[string]string{"name": pos[0], "handle": *as}, &res); err != nil {
			return err
		}
		if err := saveJoin(res); err != nil {
			return err
		}
		fmt.Printf("created network %s (%s); you are %s\n\ninvite a teammate (24h, single use):\n  lan join %s\n\ngo online:\n  lan up\n",
			res.Network.Name, res.Network.ID, res.Handle, res.Code)
		return nil

	case "invite":
		parse(fs, args, 0)
		nc, err := pickNetwork(*netSel)
		if err != nil {
			return err
		}
		var res struct{ Code string }
		if err := api("POST", "/networks/"+nc.ID+"/invites", nil, &res); err != nil {
			return err
		}
		fmt.Printf("lan join %s\n", res.Code)
		return nil

	case "join":
		pos := parse(fs, args, 1)
		var res joinResponse
		if err := api("POST", "/join", map[string]string{"code": pos[0], "handle": *as}, &res); err != nil {
			return err
		}
		if err := saveJoin(res); err != nil {
			return err
		}
		fmt.Printf("joined network %s as %s\n\ngo online:\n  lan up --net %s\n", res.Network.Name, res.Handle, res.Network.Name)
		return nil

	case "networks":
		parse(fs, args, 0)
		for _, n := range listNetworks() {
			role := "member"
			if n.Owner {
				role = "owner"
			}
			fmt.Printf("%-20s %s  as %s (%s)\n", n.Name, n.ID, n.Handle, role)
		}
		return nil

	case "up":
		var ports, dirs, conns multi
		fs.Var(&ports, "port", "share a local port: name=localport (repeatable)")
		fs.Var(&dirs, "dir", "share a folder read-only: name=path (repeatable)")
		fs.Var(&conns, "connect", "forward a peer's share: peer/share=localport (repeatable)")
		verbose := fs.Bool("v", false, "verbose tailscale logs")
		parse(fs, args, 0)
		nc, err := pickNetwork(*netSel)
		if err != nil {
			return err
		}
		return up(nc, ports, dirs, conns, *verbose)

	case "kick":
		pos := parse(fs, args, 1)
		nc, err := pickNetwork(*netSel)
		if err != nil {
			return err
		}
		if err := api("POST", "/networks/"+nc.ID+"/kick", map[string]string{"handle": pos[0]}, nil); err != nil {
			return err
		}
		fmt.Printf("removed %s from %s\n", pos[0], nc.Name)
		return nil

	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q (try `lan help`)", cmd)
}

// parse accepts flags before or after positional args and requires exactly
// npos positional args.
func parse(fs *flag.FlagSet, args []string, npos int) []string {
	var pos []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != npos {
		fs.Usage()
		os.Exit(2)
	}
	return pos
}

type joinResponse struct {
	Network struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"network"`
	Handle     string `json:"handle"`
	AuthKey    string `json:"authKey"`
	ControlURL string `json:"controlURL"`
	Owner      bool   `json:"owner"`
	Code       string `json:"code"`
}

func saveJoin(r joinResponse) error {
	nc := &NetConfig{ID: r.Network.ID, Name: r.Network.Name, Handle: r.Handle, ControlURL: r.ControlURL, AuthKey: r.AuthKey, Owner: r.Owner}
	return nc.save()
}
