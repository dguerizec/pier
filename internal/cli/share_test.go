package cli

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dguerizec/pier/internal/infra"
	"github.com/dguerizec/pier/internal/share"
)

func TestShareCommandSurface(t *testing.T) {
	cmd := newShareCmd()
	var names []string
	for _, child := range cmd.Commands() {
		names = append(names, child.Name())
	}
	sort.Strings(names)
	want := []string{"add", "hosts", "list", "remove", "url"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("subcommands = %v, want %v", names, want)
	}

	urlCmd, _, err := cmd.Find([]string{"url"})
	if err != nil {
		t.Fatal(err)
	}
	if urlCmd.Flags().Lookup("default") == nil || urlCmd.Flags().Lookup("all") == nil {
		t.Fatal("share url should expose --default and --all")
	}
}

func TestShareURLRejectsConflictingModesBeforeContextResolution(t *testing.T) {
	cmd := newShareURLCmd()
	cmd.SetArgs([]string{"--default", "--all"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v", err)
	}
}

func TestScopeAndActiveSharedRecords(t *testing.T) {
	records := []share.SharedRecord{
		{Record: share.Record{Host: "main.jobo.test", Project: "jobo", Slug: "main"}, GatewayUp: true, AddressUp: true},
		{Record: share.Record{Host: "api.main.jobo.test", Project: "jobo", Slug: "main"}, GatewayUp: false, AddressUp: true},
		{Record: share.Record{Host: "main.other.test", Project: "other", Slug: "main"}, GatewayUp: true, AddressUp: true},
	}
	scoped := scopeSharedRecords(append([]share.SharedRecord(nil), records...), "jobo", "main", false)
	if len(scoped) != 2 {
		t.Fatalf("scoped = %+v", scoped)
	}
	active, omitted := activeSharedRecords(scoped)
	if len(active) != 1 || omitted != 1 || active[0].Host != "main.jobo.test" {
		t.Fatalf("active=%+v omitted=%d", active, omitted)
	}
	all := scopeSharedRecords(append([]share.SharedRecord(nil), records...), "jobo", "main", true)
	if len(all) != 3 {
		t.Fatalf("all = %+v", all)
	}
}

func TestSharedURL(t *testing.T) {
	if got := sharedURL("main.jobo.lap.test"); got != "http://main.jobo.lap.test" {
		t.Fatalf("sharedURL = %q", got)
	}
}

func TestValidateShareAddressAllowsServerModeOnAnotherInterface(t *testing.T) {
	address := share.Address{Interface: "enp3s0", IP: "192.168.1.42"}
	cfg := &infra.Config{Mode: infra.ModeServer, BindIP: "100.125.2.23"}
	if err := validateShareAddress(cfg, address); err != nil {
		t.Fatalf("tailnet bind should allow a distinct LAN gateway: %v", err)
	}
}

func TestValidateShareAddressRejectsMainProxyCollision(t *testing.T) {
	address := share.Address{Interface: "enp3s0", IP: "192.168.1.42"}
	for _, bindIP := range []string{infra.DefaultServerBind, address.IP} {
		cfg := &infra.Config{Mode: infra.ModeServer, BindIP: bindIP}
		if err := validateShareAddress(cfg, address); err == nil {
			t.Fatalf("bind %s should be rejected", bindIP)
		}
	}
}
