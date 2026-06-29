package provider

import (
	"strings"
	"testing"

	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall_rule"
)

// The firewall_rule resource has no in-place Update (firewallRuleResource.Update
// hard-errors), so every create-only immutable must RequiresReplace or a changed value
// plans an unsupported in-place update and hard-errors. These are firewall_rule's
// immutables: path identity (project_id, location); the parent firewall reference
// (firewall_id, firewall_name); and the rule body inputs (cidr, port_range, description,
// protocol). Only id, the server-assigned identifier, is not create-only.
func TestFirewallRuleResourceRequiresReplace(t *testing.T) {
	ctx := t.Context()
	s := resource_firewall_rule.FirewallRuleResourceSchema(ctx)
	for _, name := range []string{
		"project_id", "location",
		"firewall_id", "firewall_name",
		"cidr", "port_range", "description", "protocol",
	} {
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
// as "known after apply". id is the pure computed; description, port_range and protocol are
// Optional+Computed read-back immutables that also pin (the server echoes a non-null value,
// so UseStateForUnknown holds an omitted value to prior state and RequiresReplace then
// compares equal). cidr is Required and the firewall reference / path identity are
// Optional-only, so none of them are Computed and none carry UseStateForUnknown.
func TestFirewallRuleResourceUseStateForUnknown(t *testing.T) {
	ctx := t.Context()
	s := resource_firewall_rule.FirewallRuleResourceSchema(ctx)

	pinned := []string{"id", "description", "port_range", "protocol"}
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

	// Required path identity and the Optional-only firewall reference are never Computed,
	// so they cannot go unknown and must NOT carry UseStateForUnknown (a dead no-op); they
	// take RequiresReplace alone, asserted in TestFirewallRuleResourceRequiresReplace.
	rrOnly := []string{"project_id", "location", "firewall_id", "firewall_name", "cidr"}
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

// description, port_range and protocol are Optional+Computed read-back immutables: they
// carry BOTH UseStateForUnknown and RequiresReplace, and usfu must precede rr. The
// framework threads the planned value between modifiers in slice order, so pinning to prior
// state first lets RequiresReplace compare equal values on an omitted/no-op input and not
// force a spurious replace.
func TestFirewallRuleResourceModifierOrder(t *testing.T) {
	ctx := t.Context()
	s := resource_firewall_rule.FirewallRuleResourceSchema(ctx)
	for _, name := range []string{"description", "port_range", "protocol"} {
		attr, ok := s.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q not found in schema", name)
		}
		descs := postgresPlanModifierDescriptions(ctx, attr)
		usfu := descsIndex(descs, descUseStateForUnknown)
		rr := descsIndex(descs, descRequiresReplace)
		if usfu < 0 || rr < 0 {
			t.Errorf("attribute %q: expected both modifiers, got %v", name, descs)
			continue
		}
		if usfu > rr {
			t.Errorf("attribute %q: UseStateForUnknown must precede RequiresReplace, got %v", name, descs)
		}
	}
}

// firewall_id and firewall_name are write-only create-only references to the parent
// firewall: exactly one is set, neither is echoed by the API (setFirewallRuleResourceState
// maps no firewall_id/firewall_name), so they must be Optional-only, not Optional+Computed.
// Were they Computed, the omitted member of the pair would plan as unknown-after-apply over
// a null prior state that UseStateForUnknown cannot pin (usfu returns early on null state),
// so RequiresReplace would compare unknown != null and force a spurious replace on a no-op.
// Optional-only keeps the omitted member a known null, so RequiresReplace compares
// null == null and stays quiet.
func TestFirewallRuleRefSchemaClassification(t *testing.T) {
	ctx := t.Context()
	s := resource_firewall_rule.FirewallRuleResourceSchema(ctx)
	for _, name := range []string{"firewall_id", "firewall_name"} {
		attr := s.Attributes[name]
		sa, ok := attr.(rschema.StringAttribute)
		if !ok {
			t.Fatalf("resource %s: expected rschema.StringAttribute, got %T", name, attr)
		}
		if !sa.Optional || sa.Computed {
			t.Errorf("resource %s: want Optional-only (Optional=true Computed=false), got Optional=%v Computed=%v", name, sa.Optional, sa.Computed)
		}
		for _, want := range []string{"not read back", "replacement"} {
			if !strings.Contains(sa.MarkdownDescription, want) {
				t.Errorf("resource %s MarkdownDescription must document the write-only residual (missing %q); got %q", name, want, sa.MarkdownDescription)
			}
		}
	}
}

// project_id and location are path identity: the create addresses the rule by them and the
// API never echoes them, so they are Required (matching the other resources), never
// Optional+Computed. A Required attribute is always known from config, so it never goes
// unknown and takes RequiresReplace alone.
func TestFirewallRuleIdentityRequired(t *testing.T) {
	ctx := t.Context()
	s := resource_firewall_rule.FirewallRuleResourceSchema(ctx)
	for _, name := range []string{"project_id", "location"} {
		attr := s.Attributes[name]
		sa, ok := attr.(rschema.StringAttribute)
		if !ok {
			t.Fatalf("resource %s: expected rschema.StringAttribute, got %T", name, attr)
		}
		if !sa.Required || sa.Optional || sa.Computed {
			t.Errorf("resource %s: want Required path identity (Required=true Optional=false Computed=false), got Required=%v Optional=%v Computed=%v", name, sa.Required, sa.Optional, sa.Computed)
		}
	}
}
