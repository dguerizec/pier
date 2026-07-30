package runtimevalues

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTypedAndStringTokens(t *testing.T) {
	values := Values{
		"oauth_callback_port": json.Number("49163"),
		"tenant":              "local \"dev\"",
		"enabled":             true,
	}
	input := []byte(`
preserve_ports = [{value.oauth_callback_port}]
callback = "http://127.0.0.1:{value.oauth_callback_port}/callback"
tenant = {value.tenant}
label = "tenant={value.tenant}"
enabled = {value.enabled}
# {value.missing} in comments is not rendered
`)
	got, err := Render(input, values)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"preserve_ports = [49163]",
		`callback = "http://127.0.0.1:49163/callback"`,
		`tenant = "local \"dev\""`,
		`label = "tenant=local \"dev\""`,
		"enabled = true",
		"# {value.missing} in comments is not rendered",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("rendered body missing %q:\n%s", want, got)
		}
	}
}

func TestRenderMissingValue(t *testing.T) {
	_, err := Render([]byte(`port = {value.port}`), Values{})
	if err == nil || !strings.Contains(err.Error(), `value "port"`) {
		t.Fatalf("err = %v, want missing port diagnostic", err)
	}
}

func TestHasTokensIgnoresComments(t *testing.T) {
	if HasTokens([]byte("# example: {value.port}\nport = 3000\n")) {
		t.Fatal("comment-only token should not require a resolver")
	}
	if !HasTokens([]byte(`url = "http://localhost:{value.port}"`)) {
		t.Fatal("token inside a TOML string was not detected")
	}
}

func TestResolveSaveLoadAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	valuesFile := Path(dir)
	env := MergeEnv(os.Environ(), map[string]string{
		"PIER_SLUG":        "feature-x",
		"PIER_VALUES_FILE": valuesFile,
	})
	values, err := Resolve(
		`printf '{"oauth_callback_port":49163,"slug":"%s","enabled":true}\n' "$PIER_SLUG"`,
		dir,
		env,
		nil,
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := values["slug"]; got != "feature-x" {
		t.Errorf("slug = %#v, want feature-x", got)
	}
	if err := Save(valuesFile, values); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(valuesFile)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
	loaded, err := Load(valuesFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	composeEnv, err := Environment(loaded)
	if err != nil {
		t.Fatalf("Environment: %v", err)
	}
	if got := composeEnv["PIER_VALUE_OAUTH_CALLBACK_PORT"]; got != "49163" {
		t.Errorf("PIER_VALUE_OAUTH_CALLBACK_PORT = %q, want 49163", got)
	}
	if got := composeEnv["PIER_VALUE_ENABLED"]; got != "true" {
		t.Errorf("PIER_VALUE_ENABLED = %q, want true", got)
	}
	if filepath.Dir(valuesFile) != filepath.Join(dir, ".pier") {
		t.Errorf("values path = %s", valuesFile)
	}
}

func TestResolveRejectsNestedValues(t *testing.T) {
	_, err := Resolve(`printf '{"nested":{"port":49163}}\n'`, t.TempDir(), os.Environ(), nil)
	if err == nil || !strings.Contains(err.Error(), "string, number, or boolean") {
		t.Fatalf("err = %v, want scalar-only diagnostic", err)
	}
}
