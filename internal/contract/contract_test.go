package contract

import (
	"testing"
)

// TestNormalizeSources mirrors koku _normalize_sources_acls: the Sources
// administrator role arrives as sources:*:* and must land in the flat
// `sources` bucket, not the * wildcard.
func TestNormalizeSources(t *testing.T) {
	for in, want := range map[string]string{
		"sources:*:*":               "sources:sources:*",
		"sources:*:read":            "sources:sources:read",
		"cost-management:*:*":       "cost-management:*:*",
		"cost-management:rate:read": "cost-management:rate:read",
	} {
		if got := NormalizeSourcesACL(in); got != want {
			t.Errorf("NormalizeSourcesACL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExtractPermission mirrors koku _extract_permission_data.
func TestExtractPermission(t *testing.T) {
	res, op, err := ExtractPermission("cost-management:aws.account:read")
	if err != nil {
		t.Fatalf("ExtractPermission: %v", err)
	}
	if res != "aws.account" || op != "read" {
		t.Errorf("got (%q, %q), want (aws.account, read)", res, op)
	}
	if _, _, err := ExtractPermission("bogus"); err == nil {
		t.Errorf("ExtractPermission(bogus) succeeded, want error (koku skips invalid ACLs)")
	}
}
