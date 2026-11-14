package redact

import "testing"

func TestEmail(t *testing.T) {
	tests := []struct{ in, want string }{
		{"user@example.com", "us***@ex***.com"},
		{"alice@corp.io", "al***@co***.io"},
		{"use@domain.org", "us***@do***.org"},
		{"ab@example.com", "***@ex***.com"},
		{"a@example.com", "***@ex***.com"},
		{"x@y.com", "***@***.com"},
		{"user@localhost", "us***@***"},
		{"user@.com", "us***@***"},
		{"user@", "us***@***"},
		{"@example.com", "***@ex***.com"},
		{"a@b@c.com", "a@***@***.com"},
		{"user@mail.example.com", "us***@ma***.com"},
		{"noemail", "***"},
		{"", "***"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Email(tt.in); got != tt.want {
				t.Errorf("Email(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
