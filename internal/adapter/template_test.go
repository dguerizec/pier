package adapter

import (
	"strings"
	"testing"

	"github.com/dguerizec/pier/internal/manifest"
)

func TestExpandEnv(t *testing.T) {
	c := Ctx{
		Slug:       "x",
		BaseDomain: "w3t.test",
		Expose: []manifest.ExposeRule{
			{Service: "front", Port: 8080},
			{Service: "api", Port: 8000, Host: "backend"},
		},
		DefaultService: "front",
	}
	cases := []struct {
		in, want string
	}{
		{"{slug}", "x"},
		{"{base_domain}", "w3t.test"},
		{"{host.front}", "front.x.w3t.test"},
		{"{host.api}", "backend.x.w3t.test"},
		{"{url.api}", "http://backend.x.w3t.test"},
		{"{url.default}", "http://x.w3t.test"},
		{"{host.default}", "x.w3t.test"},
		{"prefix-{slug}-suffix", "prefix-x-suffix"},
		{"{url.api}/v1?slug={slug}", "http://backend.x.w3t.test/v1?slug=x"},
		{"no tokens here", "no tokens here"},
	}
	for _, tc := range cases {
		got, err := ExpandEnv(tc.in, c)
		if err != nil {
			t.Errorf("ExpandEnv(%q) errored: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ExpandEnv(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandEnv_Errors(t *testing.T) {
	c := Ctx{
		Slug:       "x",
		BaseDomain: "w3t.test",
		Expose:     []manifest.ExposeRule{{Service: "front", Port: 8080}},
		// DefaultService intentionally empty
	}
	cases := []struct {
		in, wantSubstr string
	}{
		{"{url.ghost}", "unknown service"},
		{"{host.ghost}", "unknown service"},
		{"{bogus}", "unknown token"},
		{"{url.default}", "stack.service"},
	}
	for _, tc := range cases {
		_, err := ExpandEnv(tc.in, c)
		if err == nil {
			t.Errorf("ExpandEnv(%q) should error", tc.in)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSubstr) {
			t.Errorf("ExpandEnv(%q) err = %q, want substring %q", tc.in, err, tc.wantSubstr)
		}
	}
}

func TestExpandEnv_PierTLD(t *testing.T) {
	c := Ctx{Slug: "x", BaseDomain: "w3t.test", TLD: "test"}
	got, err := ExpandEnv("{pier.tld}", c)
	if err != nil {
		t.Fatal(err)
	}
	if got != "test" {
		t.Errorf("expansion = %q, want test", got)
	}

	c.TLD = ""
	if _, err := ExpandEnv("{pier.tld}", c); err == nil {
		t.Error("expected error when TLD is empty")
	}
}

func TestExpandPierTokens(t *testing.T) {
	cases := []struct {
		in, tld, want string
		wantErr       bool
	}{
		{"w3t.{pier.tld}", "test", "w3t.test", false},
		{"{pier.tld}", "dev", "dev", false},
		{"no tokens", "test", "no tokens", false},
		{"w3t.{pier.tld}", "", "", true}, // empty TLD
		{"{slug}.{pier.tld}", "test", "", true}, // workload-level token rejected
		{"{bogus}", "test", "", true},
	}
	for _, tc := range cases {
		got, err := ExpandPierTokens(tc.in, tc.tld)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ExpandPierTokens(%q, %q): expected error, got %q", tc.in, tc.tld, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ExpandPierTokens(%q, %q) errored: %v", tc.in, tc.tld, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ExpandPierTokens(%q, %q) = %q, want %q", tc.in, tc.tld, got, tc.want)
		}
	}
}

func TestExpandEnvBlock(t *testing.T) {
	c := Ctx{
		Slug:           "x",
		BaseDomain:     "w3t.test",
		Expose:         []manifest.ExposeRule{{Service: "api", Port: 8000}},
		DefaultService: "api",
	}
	got, err := ExpandEnvBlock(map[string]string{
		"API_URL": "{url.api}",
		"PUBLIC":  "{url.default}",
	}, c)
	if err != nil {
		t.Fatal(err)
	}
	if got["API_URL"] != "http://api.x.w3t.test" {
		t.Errorf("API_URL = %q", got["API_URL"])
	}
	if got["PUBLIC"] != "http://x.w3t.test" {
		t.Errorf("PUBLIC = %q", got["PUBLIC"])
	}
}
