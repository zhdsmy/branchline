package store

import "testing"

func TestIsAddress(t *testing.T) {
	for s, want := range map[string]bool{
		"ada@devbox:/srv/app":           true,
		"devbox:src/app":                true,
		"ssh://ada@devbox:2222/srv/app": true,
		"/home/ada/src/app":             false,
		"~/src/app":                     false,
		"~/odd:name":                    false,
		"/Volumes/a:b":                  false,
		"C:/repo":                       false,
		"":                              false,
	} {
		if got := IsAddress(s); got != want {
			t.Errorf("IsAddress(%q) = %v, want %v", s, got, want)
		}
	}
}
