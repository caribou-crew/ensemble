package server

import "testing"

// White-box test for isLoopbackAddr: lives in package server (not
// server_test) since the helper is unexported.
func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:54321", true},
		{"[::1]:54321", true},
		{"10.0.0.5:54321", false},
		{"203.0.113.7:9", false},
		{"not-an-addr", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopbackAddr(c.addr); got != c.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
