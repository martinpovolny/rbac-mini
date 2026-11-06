package contract

import (
	"testing"

	"rbac-go/internal/access"
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

// rd builds one access.Entry with a single attributeFilter, mirroring the
// /access/ data shape (and koku's LIMITED_AWS_ACCESS fixture).
func rd(permission, operation string, value any) access.Entry {
	return access.Entry{
		Permission: permission,
		ResourceDefinitions: []access.ResourceDefinition{
			{AttributeFilter: map[string]any{
				"key": "cost-management.aws.account", "operation": operation, "value": value,
			}},
		},
	}
}

// TestProcessACLs mirrors koku _process_acls: legacy rate is rewritten to
// cost_model, resourceDefinitions are extracted, invalid ACLs are skipped.
func TestProcessACLs(t *testing.T) {
	entries := []access.Entry{
		rd("cost-management:aws.account:read", "equal", "123456"),
		{Permission: "cost-management:rate:write"},
		{Permission: "bogus"},
	}
	got := ProcessACLs(entries)

	aws, ok := got["aws.account"]
	if !ok || len(aws) != 1 || aws[0].Operation != "read" {
		t.Fatalf("aws.account = %v, want [{read [123456]}]", got["aws.account"])
	}
	if len(aws[0].Resources) != 1 || aws[0].Resources[0] != "123456" {
		t.Errorf("aws.account resources = %v, want [123456]", aws[0].Resources)
	}
	cm, ok := got["cost_model"]
	if !ok || len(cm) != 1 || cm[0].Operation != "write" {
		t.Fatalf("cost_model = %v, want [{write [*]}]", got["cost_model"])
	}
	if len(cm[0].Resources) != 1 || cm[0].Resources[0] != "*" {
		t.Errorf("cost_model resources = %v, want [*]", cm[0].Resources)
	}
	if _, ok := got["bogus"]; ok {
		t.Errorf("bogus key present, want invalid ACLs skipped: %v", got)
	}
}

// TestApplyAccessWildcard mirrors koku wildcard handling: a single * ACL
// with operation * expands to every resource bucket (write where available,
// else read) and compacts to ["*"].
func TestApplyAccessWildcard(t *testing.T) {
	got := ApplyAccess(map[string][]ACLData{
		"*": {{Operation: "*", Resources: []any{"*"}}},
	})
	for _, rt := range []struct {
		name string
		ops  []string
	}{
		{"aws.account", []string{"read"}},
		{"aws.organizational_unit", []string{"read"}},
		{"gcp.account", []string{"read"}},
		{"gcp.project", []string{"read"}},
		{"azure.subscription_guid", []string{"read"}},
		{"openshift.cluster", []string{"read"}},
		{"openshift.node", []string{"read"}},
		{"openshift.project", []string{"read"}},
		{"cost_model", []string{"read", "write"}},
		{"settings", []string{"read", "write"}},
		{"sources", []string{"read", "write"}},
	} {
		bucket, ok := got[rt.name]
		if !ok {
			t.Errorf("missing bucket %q", rt.name)
			continue
		}
		for _, op := range rt.ops {
			res, ok := bucket[op]
			if !ok || len(res) != 1 || res[0] != "*" {
				t.Errorf("%s.%s = %v, want [*]", rt.name, op, bucket[op])
			}
		}
	}
}

// TestApplyAccessOCPInheritance mirrors koku _update_access_obj: a principal
// scoped to one cluster still sees node/project (restricted to *), which is
// what makes bob's cluster filter return data instead of 403.
func TestApplyAccessOCPInheritance(t *testing.T) {
	got := ApplyAccess(map[string][]ACLData{
		"openshift.cluster": {{Operation: "read", Resources: []any{"e2e-rbac-alpha-x"}}},
	})
	if got["openshift.cluster"]["read"][0] != "e2e-rbac-alpha-x" {
		t.Errorf("cluster.read = %v, want [e2e-rbac-alpha-x]", got["openshift.cluster"]["read"])
	}
	for _, res := range []string{"openshift.node", "openshift.project"} {
		read := got[res]["read"]
		if len(read) != 1 || read[0] != "*" {
			t.Errorf("%s.read = %v, want [*] via inheritance", res, read)
		}
	}
	// No inheritance without a cluster grant: empty stays empty.
	empty := ApplyAccess(map[string][]ACLData{})
	if len(empty["openshift.node"]["read"]) != 0 || len(empty["openshift.project"]["read"]) != 0 {
		t.Errorf("node/project without cluster grant = %v / %v, want empty",
			empty["openshift.node"]["read"], empty["openshift.project"]["read"])
	}
}
