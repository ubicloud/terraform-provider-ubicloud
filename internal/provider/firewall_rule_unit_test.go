package provider

import (
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall_rule"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestFirewallRuleCidrNotLiteral guards the client-side guard (issue
// fix-firewall-rule-regen-parity, point 1). The backend routes a cidr that is not a
// literal IPv4/IPv6 cidr (no "." and no ":") down the private-subnet-reference branch,
// which expands a resolvable subnet to [net4, net6] (two rules) or rejects it. A
// single-object firewall_rule resource can own neither, so the provider refuses such
// cidrs before calling the API. The predicate matches the backend's branch condition in
// helpers/firewall.rb firewall_rule_params.
func TestFirewallRuleCidrNotLiteral(t *testing.T) {
	cases := []struct {
		cidr string
		want bool
	}{
		{"0.0.0.0/0", false},
		{"1.2.3.0/24", false},
		{"10.0.0.5", false},
		{"::/0", false},
		{"fd00::/8", false},
		{"2001:db8::1", false},
		{"my-subnet", true},
		{"psqg7zbmah9csn0q3vbrpa7pk", true},
	}
	for _, c := range cases {
		if got := firewallRuleCidrNotLiteral(c.cidr); got != c.want {
			t.Errorf("firewallRuleCidrNotLiteral(%q) = %v, want %v", c.cidr, got, c.want)
		}
	}
}

// TestFirewallReferenceIsId guards the import reference routing (issue
// fix-firewall-rule-regen-parity, point 2). A firewall_reference that is a firewall UBID
// must be routed to firewall_id on import; a name must be routed to firewall_name, so the
// imported state matches how the parent firewall was referenced in config.
func TestFirewallReferenceIsId(t *testing.T) {
	cases := []struct {
		reference string
		want      bool
	}{
		{"fwfg7td83em22qfw9pq5xyfqb7", true},  // firewall ubid
		{"tf-testacc", false},                 // a name
		{"fw-production", false},              // name that starts with fw but is not a ubid
		{"fr0000000000000000000000aa", false}, // a firewall RULE ubid, not a firewall
		{"", false},
	}
	for _, c := range cases {
		if got := firewallReferenceIsId(c.reference); got != c.want {
			t.Errorf("firewallReferenceIsId(%q) = %v, want %v", c.reference, got, c.want)
		}
	}
}

// TestSetFirewallRuleResourceState guards the response read-back (issue
// fix-firewall-rule-regen-parity, point 2). After a create/read the regenerated
// Optional+Computed attributes description and protocol must be set from the response.
// firewall_id and firewall_name are the two ways to reference the parent firewall; both are
// Optional-only write-only inputs and neither appears in the FirewallRule response, so the
// mapper leaves them untouched: the reference the user set is preserved and the one left
// unset stays a known null (an Optional-only attribute never goes unknown to be coalesced).
func TestSetFirewallRuleResourceState(t *testing.T) {
	fr := ubicloud_client.FirewallRule{
		Id:          "fr0000000000000000000000aa",
		Cidr:        "1.2.3.0/24",
		PortRange:   "80..8080",
		Description: "web",
		Protocol:    ubicloud_client.Tcp,
	}

	// Typical: user references by firewall_name and omits description/protocol/firewall_id.
	byName := &resource_firewall_rule.FirewallRuleModel{
		FirewallName: types.StringValue("fw-name"),
		FirewallId:   types.StringNull(),
		Description:  types.StringUnknown(),
		Protocol:     types.StringUnknown(),
	}
	setFirewallRuleResourceState(byName, fr)

	known := map[string]types.String{
		"id": byName.Id, "cidr": byName.Cidr,
		"description": byName.Description, "protocol": byName.Protocol,
		"firewall_id": byName.FirewallId, "firewall_name": byName.FirewallName,
	}
	for name, v := range known {
		if v.IsUnknown() {
			t.Errorf("%s is still unknown after mapping", name)
		}
	}
	// port_range is a customtypes.PortRangeValue (semantic-equality string), not a plain
	// types.String, so it is checked on its own.
	if byName.PortRange.IsUnknown() {
		t.Error("port_range is still unknown after mapping")
	}
	if byName.PortRange.ValueString() != "80..8080" {
		t.Errorf("port_range = %q, want %q", byName.PortRange.ValueString(), "80..8080")
	}
	if byName.Description.ValueString() != "web" {
		t.Errorf("description = %q, want %q", byName.Description.ValueString(), "web")
	}
	if byName.Protocol.ValueString() != "tcp" {
		t.Errorf("protocol = %q, want %q", byName.Protocol.ValueString(), "tcp")
	}
	if !byName.FirewallId.IsNull() {
		t.Errorf("firewall_id should stay null when unset, got %q", byName.FirewallId.ValueString())
	}
	if byName.FirewallName.ValueString() != "fw-name" {
		t.Errorf("firewall_name not preserved, got %q", byName.FirewallName.ValueString())
	}

	// Alternate: user references by firewall_id and omits firewall_name.
	byId := &resource_firewall_rule.FirewallRuleModel{
		FirewallId:   types.StringValue("fw0000000000000000000000aa"),
		FirewallName: types.StringNull(),
	}
	setFirewallRuleResourceState(byId, fr)
	if byId.FirewallId.ValueString() != "fw0000000000000000000000aa" {
		t.Errorf("firewall_id not preserved, got %q", byId.FirewallId.ValueString())
	}
	if !byId.FirewallName.IsNull() {
		t.Errorf("firewall_name should stay null when unset, got %q", byId.FirewallName.ValueString())
	}
}

// TestFirewallReference guards the parent firewall reference selection (issue
// fix-firewall-rule-regen-parity, point 2): firewall_id is preferred when set, otherwise
// firewall_name. The firewall_reference path param accepts a firewall ID or name, so both
// resolve server-side.
func TestFirewallReference(t *testing.T) {
	cases := []struct {
		firewallId, firewallName, want string
	}{
		{"fwid", "fwname", "fwid"},
		{"", "fwname", "fwname"},
		{"fwid", "", "fwid"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := firewallReference(c.firewallId, c.firewallName); got != c.want {
			t.Errorf("firewallReference(%q, %q) = %q, want %q", c.firewallId, c.firewallName, got, c.want)
		}
	}
}
