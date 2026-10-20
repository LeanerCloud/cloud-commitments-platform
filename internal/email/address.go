package email

import "strings"

// SameAddress reports whether two email addresses are equal after trimming
// surrounding whitespace, comparing case-insensitively. Every approver and
// requester comparison must go through it so they always agree.
func SameAddress(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// ContainsAddress reports whether addrs holds an address equal to target
// under SameAddress.
func ContainsAddress(addrs []string, target string) bool {
	for _, addr := range addrs {
		if SameAddress(addr, target) {
			return true
		}
	}
	return false
}
