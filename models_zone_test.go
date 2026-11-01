package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelsZoneIsIsolatedByConstruction(t *testing.T) {
	e, u := testEnv(t, "config gateway 'main'\n\tlist zone 'lan'\n")
	fw := filepath.Join(t.TempDir(), "firewall")
	os.WriteFile(fw, []byte("config zone\n\toption name 'lan'\n\tlist network 'lan'\nconfig zone\n\toption name 'wan'\n\tlist network 'wan'\n"), 0o644)
	e.firewallPath = fw
	reloads := 0
	e.reloadFirewall = func() error { reloads++; return nil }
	var out, errOut bytes.Buffer
	if code := run([]string{"models-zone", "models"}, &out, &errOut, e); code != 0 {
		t.Fatalf("models-zone exited %d: %s", code, errOut.String())
	}
	if len(u.calls) != 1 || reloads != 1 {
		t.Fatalf("uci calls %d, firewall reloads %d; want 1 and 1", len(u.calls), reloads)
	}
	b := u.calls[0]
	for _, want := range []string{"set firewall.llm_models='zone'", "set firewall.llm_models.name='models'",
		"set firewall.llm_models.input='REJECT'", "set firewall.llm_models.forward='REJECT'",
		"set firewall.llm_models.output='ACCEPT'", "add_list firewall.llm_models.network='models'",
		"set firewall.llm_models_dhcp.dest_port='67'", "set firewall.llm_models_dns.dest_port='53'", "commit firewall"} {
		if !strings.Contains(b, want) {
			t.Fatalf("the batch lacks %q:\n%s", want, b)
		}
	}
	// No forwarding into or out of the zone: the router reaches it, nothing else does.
	if strings.Contains(b, "forwarding") {
		t.Fatalf("the batch opens a forwarding:\n%s", b)
	}
	// Internet for the model box is the owner's explicit choice, and goes to wan only.
	e2, u2 := testEnv(t, "config gateway 'main'\n")
	e2.firewallPath, e2.reloadFirewall = fw, func() error { return nil }
	if code := run([]string{"models-zone", "models", "-internet"}, &out, &errOut, e2); code != 0 {
		t.Fatalf("models-zone -internet exited %d: %s", code, errOut.String())
	}
	if c := u2.calls[0]; !strings.Contains(c, "set firewall.llm_models_wan.src='models'") || !strings.Contains(c, "set firewall.llm_models_wan.dest='wan'") {
		t.Fatalf("-internet batch:\n%s", c)
	}
}

func TestModelsZoneRefusesANetworkThatIsAlreadyZoned(t *testing.T) {
	fw := filepath.Join(t.TempDir(), "firewall")
	os.WriteFile(fw, []byte("config zone\n\toption name 'lan'\n\tlist network 'lan'\n\tlist network 'iot'\n"), 0o644)
	for _, net := range []string{"iot", "lan", "wan", "wan6", "bad'net", ""} {
		e, u := testEnv(t, "config gateway 'main'\n")
		e.firewallPath, e.reloadFirewall = fw, func() error { return nil }
		var out, errOut bytes.Buffer
		args := []string{"models-zone"}
		if net != "" {
			args = append(args, net)
		}
		if code := run(args, &out, &errOut, e); code == 0 {
			t.Fatalf("network %q: accepted", net)
		}
		if len(u.calls) != 0 {
			t.Fatalf("network %q: uci was called", net)
		}
	}
}
