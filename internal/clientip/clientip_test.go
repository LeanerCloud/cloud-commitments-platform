package clientip

import (
	"runtime"
	"strings"
	"testing"
)

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

// Keys are per /64: neighboring /64s inside one /56 are distinct keys.
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
		want   string
		hops   int
	}{
		{name: "hops 1 takes rightmost", remote: "10.0.0.1:1000", xff: "1.1.1.1, 2.2.2.2", hops: 1, want: "2.2.2.2"},
		{name: "hops 2 takes n-2", remote: "10.0.0.1:1000", xff: "9.9.9.9, 1.1.1.1, 2.2.2.2", hops: 2, want: "1.1.1.1"},
		{name: "hops 0 ignores spoofed xff", remote: "10.0.0.1:1000", xff: "6.6.6.6", hops: 0, want: "10.0.0.1"},
		{name: "fewer entries than hops uses peer", remote: "10.0.0.1:1000", xff: "2.2.2.2", hops: 2, want: "10.0.0.1"},
		{name: "no xff uses peer", remote: "10.0.0.1:1000", xff: "", hops: 1, want: "10.0.0.1"},
		{name: "garbage entry uses peer", remote: "10.0.0.1:1000", xff: "1.1.1.1, garbage", hops: 1, want: "10.0.0.1"},
		{name: "ip:port entry", remote: "10.0.0.1:1000", xff: "1.1.1.1:5555", hops: 1, want: "1.1.1.1"},
		{name: "bracketed v6 port entry", remote: "10.0.0.1:1000", xff: "[2001:db8::1]:80", hops: 1, want: "2001:db8::1"},
		{name: "bare v6 entry without port", remote: "10.0.0.1:1000", xff: "2001:db8::7", hops: 1, want: "2001:db8::7"},
		{name: "bracketed v6 without port", remote: "10.0.0.1:1000", xff: "[2001:db8::7]", hops: 1, want: "2001:db8::7"},
		{name: "v4-mapped entry unmapped", remote: "10.0.0.1:1000", xff: "::ffff:1.2.3.4", hops: 1, want: "1.2.3.4"},
		{name: "v4-mapped with zone", remote: "10.0.0.1:1000", xff: "::ffff:1.2.3.4%eth0", hops: 1, want: "1.2.3.4"},
		{name: "empty entry in the middle, hops 1", remote: "10.0.0.1:1000", xff: "1.1.1.1,,2.2.2.2", hops: 1, want: "2.2.2.2"},
		{name: "empty entry selected uses peer", remote: "10.0.0.1:1000", xff: "1.1.1.1,,2.2.2.2", hops: 2, want: "10.0.0.1"},
		{name: "peer v6 with port", remote: "[2001:db8::5]:443", xff: "", hops: 1, want: "2001:db8::5"},
		{name: "peer without port", remote: "10.0.0.1", xff: "", hops: 0, want: "10.0.0.1"},
		{name: "peer zone stripped", remote: "[fe80::1%eth0]:443", xff: "", hops: 0, want: "fe80::1"},
		{name: "entry zone stripped", remote: "10.0.0.1:1000", xff: "[fe80::1%eth0]:443", hops: 1, want: "fe80::1"},
		{name: "whitespace around entries", remote: "10.0.0.1:1000", xff: " 1.1.1.1 ,  2.2.2.2 ", hops: 1, want: "2.2.2.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.remote, tt.xff, tt.hops); got != tt.want {
				t.Fatalf("Resolve(%q, %q, %d) = %q, want %q", tt.remote, tt.xff, tt.hops, got, tt.want)
			}
		})
	}
}

// A 1MB, 100k-entry header must cost O(hops), not O(header).
func TestResolve_BoundedWork(t *testing.T) {
	xff := strings.Repeat("1.2.3.4,", 100_000) + "5.6.7.8"
	if len(xff) < 800_000 {
		t.Fatalf("header too small: %d", len(xff))
	}
	const runs = 50
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		if got := Resolve("10.0.0.1:1", xff, 2); got != "1.2.3.4" {
			t.Fatalf("got %q", got)
		}
	}
	runtime.ReadMemStats(&after)
	if perCall := (after.TotalAlloc - before.TotalAlloc) / runs; perCall > 4096 {
		t.Fatalf("Resolve allocated %d bytes per call on a large header", perCall)
	}
}
