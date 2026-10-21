package server

import (
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Agent keys from listed addresses only, a switch an admin turns on. An
// instance whose agents all call from one place (a company that only uses
// Claude's connectors, say) can say so, and a key that leaks stops being useful
// anywhere else.
//
// It covers every request that comes with a bearer key, API token and OAuth
// access token alike, on MCP and on REST: a list for MCP alone would leave the
// same key working against the REST API one path over. It does NOT cover
// browser sign-ins, the desktop app (which signs in the way a browser does),
// public links, forms or calendar feeds, so an admin cannot lock anybody out of
// the interface with it.
//
// The address is clientIP's, so behind a proxy it is only as good as that is.
// It reads the entry the proxy wrote itself. Until 1.1.2 it read the first one,
// which the caller writes, and a list like this would have been one header away
// from useless.

const (
	settingAgentAddressesOn = "agent_addresses_on"
	settingAgentAddresses   = "agent_addresses"
)

// anthropicOutbound is where Claude's connectors call from, as Anthropic
// publishes it (platform.claude.com/docs/en/api/ip-addresses, checked
// 2026-10-17). The settings offer it as one click; nothing applies it alone.
const anthropicOutbound = "160.79.104.0/21"

type addressList struct {
	mu     sync.Mutex
	raw    string
	parsed []netip.Prefix
	ok     bool
	// When each refused address was last written to the log: an agent that
	// keeps calling from the wrong place would otherwise fill the journal.
	logged map[string]time.Time
}

// parseAddressList reads one entry per line: an address or a network in CIDR
// notation, IPv4 or IPv6, with "#" starting a comment. It names the first line
// it cannot read instead of skipping it. A typo that quietly drops an entry
// locks out whoever it was written for, without a word.
func parseAddressList(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for i, line := range strings.Split(raw, "\n") {
		if j := strings.IndexByte(line, '#'); j >= 0 {
			line = line[:j]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p, ok := parseAddressEntry(line)
		if !ok {
			return nil, &addressLineError{Line: i + 1, Text: line}
		}
		out = append(out, p)
	}
	return out, nil
}

func parseAddressEntry(s string) (netip.Prefix, bool) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		// ::ffff:192.0.2.0/120 means 192.0.2.0/24; the request side is unmapped
		// too, so both have to be written the same way to ever meet.
		if a := p.Addr(); a.Is4In6() {
			if p.Bits() < 96 {
				return netip.Prefix{}, false
			}
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	a = a.Unmap().WithZone("")
	return netip.PrefixFrom(a, a.BitLen()), true
}

type addressLineError struct {
	Line int
	Text string
}

func (e *addressLineError) Error() string {
	return fmt.Sprintf("line %d: %q is not an address or a network", e.Line, e.Text)
}

// agentAddresses returns the stored list, parsed once per change.
func (s *Server) agentAddresses() ([]netip.Prefix, bool) {
	raw := s.setting(settingAgentAddresses, "")
	l := &s.agentAddressList
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.raw != raw || (l.parsed == nil && !l.ok) {
		p, err := parseAddressList(raw)
		l.raw, l.parsed, l.ok = raw, p, err == nil
	}
	return l.parsed, l.ok
}

// agentAddressAllowed reports whether a request may use an agent key from
// where it comes. With the switch off, the default, every address may. A list
// that cannot be read, or an empty one, refuses everything: the settings
// dialog cannot store either, so this only happens when somebody wrote the
// database by hand, and opening up would be the wrong surprise.
func (s *Server) agentAddressAllowed(r *http.Request) bool {
	if !s.boolSetting(settingAgentAddressesOn) {
		return true
	}
	list, ok := s.agentAddresses()
	if !ok || len(list) == 0 {
		return false
	}
	addr, err := netip.ParseAddr(s.clientIP(r))
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, p := range list {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// hasBearer reports whether the request presents an agent key at all.
func hasBearer(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// refuseAgentAddress answers a key that arrived from an address the list does
// not hold. 403 rather than 401: the key itself may be fine, and an MCP client
// that sees 401 starts signing in again, which here would go round in circles.
// The address is the caller's own, and it is the one fact the person setting
// up the agent needs to fix it.
func (s *Server) refuseAgentAddress(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	s.logRefusedAddress(ip)
	httpErrorData(w, http.StatusForbidden, "agent_address_not_allowed",
		"This instance accepts agent keys only from listed addresses, and "+ip+" is not one of them. An admin can add it under Instance settings, Access.",
		map[string]any{"address": ip})
}

// A refused address is logged at most every ten minutes. Not as "auth:
// rejected …": that line is fail2ban's, and a jail reading it would ban an
// employee's home address at the firewall for running an agent from there.
func (s *Server) logRefusedAddress(ip string) {
	l := &s.agentAddressList
	l.mu.Lock()
	if l.logged == nil || len(l.logged) > 1000 {
		l.logged = map[string]time.Time{}
	}
	last, seen := l.logged[ip]
	if seen && time.Since(last) < 10*time.Minute {
		l.mu.Unlock()
		return
	}
	l.logged[ip] = time.Now()
	l.mu.Unlock()
	log.Printf("agents: key refused from %s, not on the address list", ip)
}

// validAgentAddresses checks what the settings dialog is about to store and
// answers the error itself. The switch cannot go on over an empty list: that
// would refuse every key, which is better done by not handing any out.
func (s *Server) validAgentAddresses(w http.ResponseWriter, on *bool, raw *string) bool {
	text := s.setting(settingAgentAddresses, "")
	if raw != nil {
		text = *raw
	}
	list, err := parseAddressList(text)
	if err != nil {
		le := err.(*addressLineError)
		httpErrorData(w, 400, "agent_addresses_invalid", "The address list has an entry that is neither an address nor a network: "+err.Error()+".",
			map[string]any{"line": le.Line, "entry": le.Text})
		return false
	}
	enabled := s.boolSetting(settingAgentAddressesOn)
	if on != nil {
		enabled = *on
	}
	if enabled && len(list) == 0 {
		httpErrorCode(w, 400, "agent_addresses_empty", "Add at least one address or network before accepting agent keys only from listed addresses.")
		return false
	}
	return true
}
