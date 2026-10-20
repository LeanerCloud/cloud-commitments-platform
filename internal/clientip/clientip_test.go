package clientip

import "testing"

func TestRateLimitKey(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"ipv4", "192.0.2.7", "192.0.2.7"},
		{"ipv4 with port", "192.0.2.7:5555", "192.0.2.7"},
		{"ipv4 mapped", "::ffff:192.0.2.7", "192.0.2.7"},
		{"ipv4 mapped with zone", "::ffff:192.0.2.7%eth0", "192.0.2.7"},
		{"ipv4 mapped bracket port", "[::ffff:192.0.2.7]:80", "192.0.2.7"},
		{"ipv6 bare", "2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"ipv6 same /64", "2001:db8:1:2:1111:2222:3333:4444", "2001:db8:1:2::/64"},
		{"ipv6 adjacent /64", "2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"ipv6 bracket port", "[2001:db8:1:2::9]:443", "2001:db8:1:2::/64"},
		{"ipv6 bracket no port", "[2001:db8:1:2::9]", "2001:db8:1:2::/64"},
		{"ipv6 zone", "fe80::1%eth0", "fe80::/64"},
		{"ipv6 loopback", "[::1]:80", "::/64"},
		{"empty", "", "invalid"},
		{"garbage", "abc", "invalid"},
		{"host port garbage", "abc:80", "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RateLimitKey(tt.in); got != tt.want {
				t.Fatalf("RateLimitKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A /56 or /48 allocation still lets a client rotate across /64s; masking
// narrower than the allocation is a known residual limit, not a bug.
func TestRateLimitKey_RotationAcross56StillWorks(t *testing.T) {
	if RateLimitKey("2001:db8:1:100::1") == RateLimitKey("2001:db8:1:101::1") {
		t.Fatal("adjacent /64s inside one /56 must remain distinct keys")
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		xff    string
		hops   int
		want   string
	}{
		{"hops 1 takes rightmost", "10.0.0.1:1000", "1.1.1.1, 2.2.2.2", 1, "2.2.2.2"},
		{"hops 2 takes n-2", "10.0.0.1:1000", "9.9.9.9, 1.1.1.1, 2.2.2.2", 2, "1.1.1.1"},
		{"hops 0 ignores spoofed xff", "10.0.0.1:1000", "6.6.6.6", 0, "10.0.0.1"},
		{"fewer entries than hops uses peer", "10.0.0.1:1000", "2.2.2.2", 2, "10.0.0.1"},
		{"no xff uses peer", "10.0.0.1:1000", "", 1, "10.0.0.1"},
		{"garbage entry uses peer", "10.0.0.1:1000", "1.1.1.1, garbage", 1, "10.0.0.1"},
		{"ip:port entry", "10.0.0.1:1000", "1.1.1.1:5555", 1, "1.1.1.1"},
		{"bracketed v6 port entry", "10.0.0.1:1000", "[2001:db8::1]:80", 1, "2001:db8::1"},
		{"bare v6 entry without port", "10.0.0.1:1000", "2001:db8::7", 1, "2001:db8::7"},
		{"bracketed v6 without port", "10.0.0.1:1000", "[2001:db8::7]", 1, "2001:db8::7"},
		{"v4-mapped entry unmapped", "10.0.0.1:1000", "::ffff:1.2.3.4", 1, "1.2.3.4"},
		{"v4-mapped with zone", "10.0.0.1:1000", "::ffff:1.2.3.4%eth0", 1, "1.2.3.4"},
		{"empty entry in the middle, hops 1", "10.0.0.1:1000", "1.1.1.1,,2.2.2.2", 1, "2.2.2.2"},
		{"empty entry selected uses peer", "10.0.0.1:1000", "1.1.1.1,,2.2.2.2", 2, "10.0.0.1"},
		{"peer v6 with port", "[2001:db8::5]:443", "", 1, "2001:db8::5"},
		{"peer without port", "10.0.0.1", "", 0, "10.0.0.1"},
		{"whitespace around entries", "10.0.0.1:1000", " 1.1.1.1 ,  2.2.2.2 ", 1, "2.2.2.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.remote, tt.xff, tt.hops); got != tt.want {
				t.Fatalf("Resolve(%q, %q, %d) = %q, want %q", tt.remote, tt.xff, tt.hops, got, tt.want)
			}
		})
	}
}
