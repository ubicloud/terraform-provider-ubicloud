package provider

import (
	"testing"

	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_private_subnet"
)

// The private_subnet resource has no in-place Update (privateSubnetResource.Update
// hard-errors), so every create-only immutable must RequiresReplace or a changed value
// plans an unsupported in-place update and hard-errors. These are private_subnet's
// immutables: path identity (project_id, location, name) and the write-only firewall_id
// create input (the firewall attached at creation).
func TestPrivateSubnetResourceRequiresReplace(t *testing.T) {
	ctx := t.Context()
	s := resource_private_subnet.PrivateSubnetResourceSchema(ctx)
	for _, name := range []string{"project_id", "location", "name", "firewall_id"} {
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

// Stable read-back computeds must pin to prior state so a no-op apply does not churn them as
// "known after apply": id, net4, net6, state, and the two nested lists (nics, firewalls).
// project_id/location/name (Required) and firewall_id (Optional-only, write-only) are never
// Computed, so they cannot go unknown and must NOT carry UseStateForUnknown.
func TestPrivateSubnetResourceUseStateForUnknown(t *testing.T) {
	ctx := t.Context()
	s := resource_private_subnet.PrivateSubnetResourceSchema(ctx)

	pinned := []string{"id", "net4", "net6", "state", "nics", "firewalls"}
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

	rrOnly := []string{"project_id", "location", "name", "firewall_id"}
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

// firewall_id is a write-only create input (the firewall attached at creation, never echoed
// back: setPrivateSubnetStateResource maps no firewall_id and surfaces the attachment via the
// firewalls list). It must stay Optional-only: were it Optional+Computed, an omitted value
// would plan unknown over a null prior state that UseStateForUnknown cannot pin, so the new
// RequiresReplace would force a spurious replace on a no-op.
func TestPrivateSubnetFirewallIdClassification(t *testing.T) {
	ctx := t.Context()
	attr := resource_private_subnet.PrivateSubnetResourceSchema(ctx).Attributes["firewall_id"]
	sa, ok := attr.(rschema.StringAttribute)
	if !ok {
		t.Fatalf("resource firewall_id: expected rschema.StringAttribute, got %T", attr)
	}
	if !sa.Optional || sa.Computed {
		t.Errorf("resource firewall_id: want Optional-only (Optional=true Computed=false), got Optional=%v Computed=%v", sa.Optional, sa.Computed)
	}
}
