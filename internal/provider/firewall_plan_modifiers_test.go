package provider

import (
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall"
)

// The firewall resource has no in-place Update (firewallResource.Update hard-errors), so
// every create-only immutable must RequiresReplace or a changed value plans an unsupported
// in-place update and hard-errors. These are firewall's immutables: path identity
// (project_id, location, name) and the Optional+Computed create input the server echoes
// back (description).
func TestFirewallResourceRequiresReplace(t *testing.T) {
	ctx := t.Context()
	s := resource_firewall.FirewallResourceSchema(ctx)
	for _, name := range []string{"project_id", "location", "name", "description"} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		if !descsContain(descs, descRequiresReplace) {
			t.Errorf("attribute %q: expected RequiresReplace plan modifier, got %v", name, descs)
		}
	}
}

// Stable read-back computeds must pin to prior state so a no-op apply does not churn them
// as "known after apply". id and the two nested lists (firewall_rules, private_subnets) are
// pure computeds; description is an Optional+Computed read-back immutable that also pins (the
// server echoes a non-null value, so UseStateForUnknown holds an omitted value to prior
// state and RequiresReplace then compares equal). project_id/location/name are Required, so
// they never go unknown and must NOT carry UseStateForUnknown.
func TestFirewallResourceUseStateForUnknown(t *testing.T) {
	ctx := t.Context()
	s := resource_firewall.FirewallResourceSchema(ctx)

	pinned := []string{"id", "firewall_rules", "private_subnets", "description"}
	for _, name := range pinned {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		if !descsContain(descs, descUseStateForUnknown) {
			t.Errorf("attribute %q: expected UseStateForUnknown plan modifier, got %v", name, descs)
		}
	}

	rrOnly := []string{"project_id", "location", "name"}
	for _, name := range rrOnly {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		if descsContain(descs, descUseStateForUnknown) {
			t.Errorf("attribute %q: expected NO UseStateForUnknown (never goes unknown), got %v", name, descs)
		}
	}
}

// description is an Optional+Computed read-back immutable: it carries BOTH UseStateForUnknown
// and RequiresReplace, and usfu must precede rr so RequiresReplace compares an omitted value
// against prior state (equal) instead of unknown (spurious replace).
func TestFirewallResourceModifierOrder(t *testing.T) {
	ctx := t.Context()
	s := resource_firewall.FirewallResourceSchema(ctx)
	attr, ok := s.Attributes["description"]
	if !ok {
		t.Fatal("attribute \"description\" not found in schema")
	}
	descs := postgresPlanModifierDescriptions(ctx, attr)
	usfu := descsIndex(descs, descUseStateForUnknown)
	rr := descsIndex(descs, descRequiresReplace)
	if usfu < 0 || rr < 0 {
		t.Fatalf("description: expected both modifiers, got %v", descs)
	}
	if usfu > rr {
		t.Errorf("description: UseStateForUnknown must precede RequiresReplace, got %v", descs)
	}
}
