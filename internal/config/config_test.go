package config

import "testing"

func TestNormalizeRunAs(t *testing.T) {
	cases := map[string]string{"": "auto", " User ": "user", "SERVICE": "service", "bogus": "auto"}
	for in, want := range cases {
		s := AgentSettings{Apps: []Application{{Name: "a", Path: `C:\a\a.exe`, RunAs: in}}}
		NormalizeSettings(&s)
		if got := s.Apps[0].RunAs; got != want {
			t.Errorf("RunAs %q normalized to %q, want %q", in, got, want)
		}
	}
}
