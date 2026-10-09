package credentials

import (
	"errors"
	"fmt"
)

// Categories reported by ClassifyGCPCredential in addition to the
// GCPCredentialKind values.
const (
	GCPAuditOK            = "ok"
	GCPAuditDecryptFailed = "decrypt-failed"
)

// ClassifyGCPCredential maps a stored GCP credential configuration to a coarse
// category for auditing: "ok" or the kind of the first validation failure.
func ClassifyGCPCredential(credType string, raw []byte) string {
	err := ValidateGCPCredentialJSON(credType, raw)
	if err == nil {
		return GCPAuditOK
	}
	var ge *GCPCredentialError
	if errors.As(err, &ge) {
		return string(ge.Kind)
	}
	return string(GCPKindOther)
}

// FormatGCPAuditLine renders one audit result. It takes no payload data, so
// the output cannot contain credential content.
func FormatGCPAuditLine(source, id, credType, category string) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s", source, id, credType, category)
}
