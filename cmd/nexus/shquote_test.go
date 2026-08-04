package main

import "testing"

func TestShJoin(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		// Plain argv — safe tokens pass through unquoted.
		{[]string{"./backup.sh", "--year", "2024"}, "./backup.sh --year 2024"},
		// A quoted shell script must survive as one argument, not be flattened
		// (the #81 bug: naive space-join corrupted the command).
		{[]string{"/bin/sh", "-c", "echo L1; echo L2; echo L3"}, "/bin/sh -c 'echo L1; echo L2; echo L3'"},
		// Spaces and other metacharacters force quoting.
		{[]string{"echo", "a b"}, "echo 'a b'"},
		{[]string{"grep", "a|b"}, "grep 'a|b'"},
		// Embedded single quotes are escaped.
		{[]string{"echo", "it's"}, `echo 'it'\''s'`},
		// Empty argument is preserved as ''.
		{[]string{"x", ""}, "x ''"},
	}
	for _, c := range cases {
		if got := shJoin(c.in); got != c.want {
			t.Errorf("shJoin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
