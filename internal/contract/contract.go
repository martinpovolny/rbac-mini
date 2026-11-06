// Package contract ports koku's read-side ACL mapping
// (koku/koku/rbac.py: _normalize_sources_acls, _extract_permission_data,
// _extract_resource_definitions, _process_acls) so the Go test suite proves
// rbac-go's /access/ output is consumable by koku's mapping.
package contract

import (
	"errors"
	"strings"

	"rbac-go/internal/access"
)

// NormalizeSourcesACL rewrites sources:<resource>:<op> to
// sources:sources:<op> (koku _normalize_sources_acls).
func NormalizeSourcesACL(permission string) string {
	parts := strings.Split(permission, ":")
	if len(parts) == 3 && parts[0] == "sources" {
		return "sources:sources:" + parts[2]
	}
	return permission
}

// ExtractPermission splits app:resource:verb (koku _extract_permission_data).
func ExtractPermission(permission string) (resource, operation string, err error) {
	parts := strings.Split(permission, ":")
	if len(parts) != 3 {
		return "", "", errors.New("invalid permission definition")
	}
	return parts[1], parts[2], nil
}

// ACLData is one grouped capability (koku acl_data).
type ACLData struct {
	Operation string
	Resources []any
}

// ProcessACLs groups /access/ entries by resource type (koku _process_acls):
// legacy cost-management:rate is rewritten to cost_model, malformed
// permissions are skipped, empty resourceDefinitions mean ["*"].
func ProcessACLs(entries []access.Entry) map[string][]ACLData {
	out := map[string][]ACLData{}
	for _, e := range entries {
		permission := e.Permission
		if strings.Contains(permission, "cost-management:rate") {
			permission = strings.Replace(permission, "rate", "cost_model", 1)
		}
		resTyp, operation, err := ExtractPermission(permission)
		if err != nil {
			continue
		}
		out[resTyp] = append(out[resTyp], ACLData{
			Operation: operation,
			Resources: extractResources(e.ResourceDefinitions),
		})
	}
	return out
}

// ResourceType mirrors koku RESOURCE_TYPES (on-prem list, sources included).
type ResourceType struct {
	Name       string
	Operations []string
}

// resourceTypes mirrors koku RESOURCE_TYPES with the on-prem sources entry.
var resourceTypes = []ResourceType{
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
}

// ApplyAccess folds grouped capabilities into the per-resource access map
// koku enforces (koku _apply_access): wildcard expansion, OCP inheritance,
// "*" compaction.
func ApplyAccess(access map[string][]ACLData) map[string]map[string][]any {
	resAccess := map[string]map[string][]any{}
	for _, rt := range resourceTypes {
		m := map[string][]any{}
		for _, op := range rt.Operations {
			m[op] = []any{}
		}
		resAccess[rt.Name] = m
	}
	if access == nil {
		return resAccess
	}

	expanded := make(map[string][]ACLData, len(access))
	for k, v := range access {
		expanded[k] = append([]ACLData{}, v...)
	}
	for _, w := range expanded["*"] {
		for _, rt := range resourceTypes {
			op := w.Operation
			if op == "*" {
				op = rt.Operations[len(rt.Operations)-1]
			}
			expanded[rt.Name] = append(expanded[rt.Name], ACLData{Operation: op, Resources: w.Resources})
		}
	}

	updateAccessObj(expanded, resAccess)

	for _, rt := range resourceTypes {
		for _, op := range rt.Operations {
			for _, item := range resAccess[rt.Name][op] {
				if item == "*" {
					resAccess[rt.Name][op] = []any{"*"}
					break
				}
			}
		}
	}
	return resAccess
}

func updateAccessObj(access map[string][]ACLData, resAccess map[string]map[string][]any) {
	for _, rt := range resourceTypes {
		items := access[rt.Name]
		// OCP inheritance: cluster->node->project when the higher level is
		// restricted (koku _update_access_obj).
		if rt.Name == "openshift.node" && len(items) == 0 && len(resAccess["openshift.cluster"]["read"]) != 0 {
			items = []ACLData{{Operation: "read", Resources: []any{"*"}}}
		}
		if rt.Name == "openshift.project" && len(items) == 0 && len(resAccess["openshift.node"]["read"]) != 0 {
			items = []ACLData{{Operation: "read", Resources: []any{"*"}}}
		}
		for _, item := range items {
			op := item.Operation
			if op == "*" {
				op = rt.Operations[len(rt.Operations)-1]
			}
			if op == "write" {
				if _, ok := resAccess[rt.Name]["write"]; ok {
					resAccess[rt.Name]["write"] = append(resAccess[rt.Name]["write"], item.Resources...)
					resAccess[rt.Name]["read"] = append(resAccess[rt.Name]["read"], item.Resources...)
				}
			}
			if op == "read" {
				resAccess[rt.Name]["read"] = append(resAccess[rt.Name]["read"], item.Resources...)
			}
		}
	}
}

// extractResources mirrors koku _extract_resource_definitions.
func extractResources(rds []access.ResourceDefinition) []any {
	if len(rds) == 0 {
		return []any{"*"}
	}
	var out []any
	for _, rd := range rds {
		af := rd.AttributeFilter
		op, _ := af["operation"].(string)
		value := af["value"]
		switch op {
		case "equal":
			if value != nil && value != "" {
				out = append(out, value)
			}
		case "in":
			switch v := value.(type) {
			case string:
				if v != "" {
					for _, s := range strings.Split(v, ",") {
						out = append(out, s)
					}
				}
			case []any:
				out = append(out, v...)
			}
		}
	}
	return out
}
