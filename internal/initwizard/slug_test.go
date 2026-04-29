package initwizard

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"MyApp":          "myapp",
		"hello world":    "hello-world",
		"  Foo__Bar  ":   "foo-bar",
		"---x---":        "x",
		"":               "",
		"under_score":    "under-score",
		"a/b/c":          "a-b-c",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateName(t *testing.T) {
	ok := []string{"app", "my-app", "a", "a1", "a-b-c", "abc123"}
	bad := []string{"", "-app", "app-", "App", "my_app", "a.b", "a b"}
	for _, n := range ok {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}
