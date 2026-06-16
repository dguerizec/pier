package systemd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	body := Render("/usr/local/bin/pier")
	mustContain(t, body, "ExecStart=/usr/local/bin/pier serve")
	mustContain(t, body, "WantedBy=default.target")
	mustContain(t, body, "After=default.target")
	mustContain(t, body, "Restart=on-failure")
	// User unit inherits HOME from the systemd --user manager — must
	// not bake an explicit User= or HOME or systemd rejects it.
	mustNotContain(t, body, "User=")
	mustNotContain(t, body, "Environment=HOME=")
	// Cross-scope dep would be rejected — stay scoped to default.target.
	mustNotContain(t, body, "After=docker.service")
	mustNotContain(t, body, "WantedBy=multi-user.target")
}

func TestQuerySystem_Missing(t *testing.T) {
	oldPath := systemUnitPath
	systemUnitPath = filepath.Join(t.TempDir(), "pier.service")
	defer func() { systemUnitPath = oldPath }()

	st := QuerySystem()
	if st.Loaded || st.Active || st.Enabled {
		t.Fatalf("QuerySystem missing = %+v, want empty status", st)
	}
	if st.Path != systemUnitPath {
		t.Fatalf("QuerySystem path = %q, want %q", st.Path, systemUnitPath)
	}
}

func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("body missing %q\n--body--\n%s", want, body)
	}
}

func mustNotContain(t *testing.T, body, unwanted string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Fatalf("body unexpectedly contains %q\n--body--\n%s", unwanted, body)
	}
}
