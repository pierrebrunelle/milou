package router

import "testing"

func TestParsePrompt(t *testing.T) {
	cases := []struct{ in, tag, clean string }{
		{"@pi fix the bug", "pi", "fix the bug"},
		{"  @mojo  add tests  ", "mojo", "add tests"},
		{"no tag here", "", "no tag here"},
		{"email me@x.com", "", "email me@x.com"},
		{"@solo", "solo", ""},
	}
	for _, c := range cases {
		tag, clean := ParsePrompt(c.in)
		if tag != c.tag || clean != c.clean {
			t.Fatalf("ParsePrompt(%q) = (%q,%q), want (%q,%q)", c.in, tag, clean, c.tag, c.clean)
		}
	}
}
