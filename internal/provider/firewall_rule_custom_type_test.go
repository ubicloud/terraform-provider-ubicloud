package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/customtypes"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall_rule"
)

// TestFirewallRulePortRangeCustomType asserts the generated firewall_rule schema attaches
// the PortRangeType custom type to port_range. The backend collapses an equal-bounds port
// range to a single port (model/firewall_rule.rb display_port_range), so without the
// custom type's semantic equality an apply of port_range="22..22" fails "inconsistent
// result after apply". The custom type is injected via config/custom_types.jq so it
// regenerates into the schema and model rather than being hand-patched.
func TestFirewallRulePortRangeCustomType(t *testing.T) {
	ctx := t.Context()
	s := resource_firewall_rule.FirewallRuleResourceSchema(ctx)
	attr, ok := s.Attributes["port_range"]
	if !ok {
		t.Fatal(`attribute "port_range" not found in schema`)
	}
	sa, ok := attr.(schema.StringAttribute)
	if !ok {
		t.Fatalf("port_range is %T, want schema.StringAttribute", attr)
	}
	if sa.CustomType == nil {
		t.Fatal("port_range has no CustomType; expected customtypes.PortRangeType")
	}
	if !sa.CustomType.Equal(customtypes.PortRangeType{}) {
		t.Errorf("port_range CustomType = %v, want customtypes.PortRangeType", sa.CustomType)
	}
}
