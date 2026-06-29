package provider

import (
	"encoding/json"
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_firewall"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// sampleDetailedFirewall mimics getLocationFirewallDetails: the base Firewall fields plus
// the detailed-only private_subnets list (openapi FirewallDetailed). Each attached subnet
// is a full PrivateSubnet object (id, name, state, location, net4, net6, a recursive
// firewalls list, and nics), because the serializer has always emitted objects. The
// recursive per-subnet firewalls are intentionally populated here to prove the mapping
// accepts them and drops them (the schema mirrors ubi fw show, which omits them). The
// create response omits private_subnets, so both the resource Create and Read paths must
// produce a KNOWN private_subnets value or apply fails "invalid result object".
func sampleDetailedFirewall() ubicloud_client.FirewallDetailed {
	vmName := "tf-acc-vm"
	return ubicloud_client.FirewallDetailed{
		Id:          "fwfg7td83em22qfw9pq5xyfqb7",
		Name:        "tf-acc-fw",
		Location:    "aws-us-east-1",
		Description: "acc firewall",
		FirewallRules: []ubicloud_client.FirewallRule{
			{Id: "fr0000000000000000000000aa", Cidr: "0.0.0.0/0", PortRange: "22..22", Protocol: ubicloud_client.Tcp},
		},
		PrivateSubnets: []ubicloud_client.PrivateSubnet{
			{
				Id:       "psaaaaaaaaaaaaaaaaaaaaaaaa",
				Name:     "tf-acc-ps-a",
				State:    "available",
				Location: "aws-us-east-1",
				Net4:     "10.0.0.0/26",
				Net6:     "fd00::/64",
				Firewalls: []ubicloud_client.Firewall{
					{
						Id:          "fwfg7td83em22qfw9pq5xyfqb7",
						Name:        "tf-acc-fw",
						Location:    "aws-us-east-1",
						Description: "acc firewall",
						FirewallRules: []ubicloud_client.FirewallRule{
							{Id: "fr0000000000000000000000aa", Cidr: "0.0.0.0/0", PortRange: "22..22", Protocol: ubicloud_client.Tcp},
						},
					},
				},
				Nics: []ubicloud_client.Nic{
					{Id: "ncaaaaaaaaaaaaaaaaaaaaaaaa", Name: "tf-acc-nic", PrivateIpv4: "10.0.0.5", PrivateIpv6: "fd00::5", VmName: &vmName},
				},
			},
			{
				Id:        "psbbbbbbbbbbbbbbbbbbbbbbbb",
				Name:      "tf-acc-ps-b",
				State:     "available",
				Location:  "aws-us-east-1",
				Net4:      "10.0.1.0/26",
				Net6:      "fd00:1::/64",
				Firewalls: []ubicloud_client.Firewall{},
				Nics:      []ubicloud_client.Nic{},
			},
		},
	}
}

// assertFirstSubnetMapped checks that private_subnets carries two nested objects and that
// the first one's scalar fields and nested nic survived the mapping. Both the resource and
// data source share GetPrivateSubnetsState, so the elements are datasource_firewall values
// in either model.
func assertFirstSubnetMapped(t *testing.T, list types.List) {
	t.Helper()
	if list.IsUnknown() || list.IsNull() || len(list.Elements()) != 2 {
		t.Fatalf("private_subnets unknown=%v null=%v len=%d, want 2", list.IsUnknown(), list.IsNull(), len(list.Elements()))
	}

	ps, ok := list.Elements()[0].(datasource_firewall.PrivateSubnetsValue)
	if !ok {
		t.Fatalf("private_subnets[0] is %T, want datasource_firewall.PrivateSubnetsValue", list.Elements()[0])
	}
	for name, got := range map[string]string{
		"id":       ps.Id.ValueString(),
		"name":     ps.Name.ValueString(),
		"state":    ps.State.ValueString(),
		"location": ps.Location.ValueString(),
		"net4":     ps.Net4.ValueString(),
		"net6":     ps.Net6.ValueString(),
	} {
		want := map[string]string{
			"id":       "psaaaaaaaaaaaaaaaaaaaaaaaa",
			"name":     "tf-acc-ps-a",
			"state":    "available",
			"location": "aws-us-east-1",
			"net4":     "10.0.0.0/26",
			"net6":     "fd00::/64",
		}[name]
		if got != want {
			t.Errorf("private_subnets[0].%s = %q, want %q", name, got, want)
		}
	}

	if ps.Nics.IsUnknown() || ps.Nics.IsNull() || len(ps.Nics.Elements()) != 1 {
		t.Fatalf("private_subnets[0].nics unknown=%v null=%v len=%d, want 1", ps.Nics.IsUnknown(), ps.Nics.IsNull(), len(ps.Nics.Elements()))
	}
	nic, ok := ps.Nics.Elements()[0].(datasource_firewall.NicsValue)
	if !ok {
		t.Fatalf("private_subnets[0].nics[0] is %T, want datasource_firewall.NicsValue", ps.Nics.Elements()[0])
	}
	if nic.PrivateIpv4.ValueString() != "10.0.0.5" {
		t.Errorf("nics[0].private_ipv4 = %q, want 10.0.0.5", nic.PrivateIpv4.ValueString())
	}
	if nic.VmName.ValueString() != "tf-acc-vm" {
		t.Errorf("nics[0].vm_name = %q, want tf-acc-vm", nic.VmName.ValueString())
	}
}

// TestSetFirewallStateResource guards the read-back of the firewall resource's computed
// surface (issue fix-firewall-resource-unmapped-computeds). private_subnets is Computed in
// the schema but was never mapped, so it stayed unknown after apply. After mapping, every
// computed must be known and private_subnets must carry the response's nested subnets.
func TestSetFirewallStateResource(t *testing.T) {
	ctx := t.Context()
	fw := sampleDetailedFirewall()
	var m resource_firewall.FirewallModel
	if diags := setFirewallStateResource(ctx, &fw, &m); diags.HasError() {
		t.Fatalf("setFirewallStateResource diags: %+v", diags)
	}

	assertFirstSubnetMapped(t, m.PrivateSubnets)

	for name, v := range map[string]types.String{"id": m.Id, "name": m.Name, "location": m.Location, "description": m.Description} {
		if v.IsUnknown() {
			t.Errorf("%s is unknown after mapping", name)
		}
	}
	if m.Id.ValueString() != "fwfg7td83em22qfw9pq5xyfqb7" {
		t.Errorf("id = %q, want the response ubid", m.Id.ValueString())
	}
	if m.FirewallRules.IsUnknown() || m.FirewallRules.IsNull() || len(m.FirewallRules.Elements()) != 1 {
		t.Errorf("firewall_rules unknown=%v null=%v len=%d, want 1", m.FirewallRules.IsUnknown(), m.FirewallRules.IsNull(), len(m.FirewallRules.Elements()))
	}
}

// TestSetFirewallStateResourceEmptySubnets covers the create path: a firewall created
// through the API is attached to no subnet, so the synthesized detailed view has an empty
// list. That must map to a KNOWN empty list (not unknown, not null), matching the
// detailed-GET of a zero-subnet firewall (private_subnets: []).
func TestSetFirewallStateResourceEmptySubnets(t *testing.T) {
	ctx := t.Context()
	fw := sampleDetailedFirewall()
	fw.PrivateSubnets = []ubicloud_client.PrivateSubnet{}
	var m resource_firewall.FirewallModel
	if diags := setFirewallStateResource(ctx, &fw, &m); diags.HasError() {
		t.Fatalf("setFirewallStateResource diags: %+v", diags)
	}

	if m.PrivateSubnets.IsUnknown() {
		t.Fatal("private_subnets is unknown for a zero-subnet firewall")
	}
	if m.PrivateSubnets.IsNull() {
		t.Fatal("private_subnets is null, want a known empty list")
	}
	if len(m.PrivateSubnets.Elements()) != 0 {
		t.Errorf("private_subnets len=%d, want 0", len(m.PrivateSubnets.Elements()))
	}
}

// TestSetFirewallStateDatasource guards the same private_subnets mapping on the data
// source, whose Read already fetches the detailed response. Before the spec sync the
// client typed private_subnets as []string, so a firewall with >=1 subnet failed to
// json-unmarshal the object array; now it maps the nested PrivateSubnet objects.
func TestSetFirewallStateDatasource(t *testing.T) {
	ctx := t.Context()
	fw := sampleDetailedFirewall()
	var m datasource_firewall.FirewallModel
	if diags := setFirewallStateDatasource(ctx, &fw, &m); diags.HasError() {
		t.Fatalf("setFirewallStateDatasource diags: %+v", diags)
	}

	assertFirstSubnetMapped(t, m.PrivateSubnets)

	if m.Id.ValueString() != "fwfg7td83em22qfw9pq5xyfqb7" {
		t.Errorf("id = %q, want the response ubid", m.Id.ValueString())
	}
}

// TestFirewallDetailedUnmarshalsNestedPrivateSubnets is the on-the-wire guard for the bug
// the spec sync fixes. Serializers::Firewall has always emitted private_subnets as full
// PrivateSubnet objects (with nested firewalls and nics), but the client typed them as
// []string, so the data source Read failed to json-unmarshal any firewall with >=1 subnet.
// The regenerated client types private_subnets as []PrivateSubnet; this decodes a
// representative detailed response and asserts the nested objects survive. With the stale
// []string typing this body fails to unmarshal ("cannot unmarshal object into Go struct
// field ... of type string").
func TestFirewallDetailedUnmarshalsNestedPrivateSubnets(t *testing.T) {
	const body = `{
		"id": "fwfg7td83em22qfw9pq5xyfqb7",
		"name": "tf-acc-fw",
		"description": "acc firewall",
		"location": "aws-us-east-1",
		"firewall_rules": [
			{"id": "fr0000000000000000000000aa", "cidr": "0.0.0.0/0", "port_range": "22..22", "protocol": "tcp"}
		],
		"private_subnets": [
			{
				"id": "psaaaaaaaaaaaaaaaaaaaaaaaa",
				"name": "tf-acc-ps-a",
				"state": "available",
				"location": "aws-us-east-1",
				"net4": "10.0.0.0/26",
				"net6": "fd00::/64",
				"firewalls": [
					{"id": "fwfg7td83em22qfw9pq5xyfqb7", "name": "tf-acc-fw", "description": "acc firewall", "location": "aws-us-east-1", "firewall_rules": [{"id": "fr0000000000000000000000aa", "cidr": "0.0.0.0/0", "port_range": "22..22", "protocol": "tcp"}]}
				],
				"nics": [
					{"id": "ncaaaaaaaaaaaaaaaaaaaaaaaa", "name": "tf-acc-nic", "private_ipv4": "10.0.0.5", "private_ipv6": "fd00::5", "vm_name": "tf-acc-vm"}
				]
			}
		]
	}`

	var fw ubicloud_client.FirewallDetailed
	if err := json.Unmarshal([]byte(body), &fw); err != nil {
		t.Fatalf("unmarshal detailed firewall with object[] private_subnets: %v", err)
	}

	if len(fw.PrivateSubnets) != 1 {
		t.Fatalf("private_subnets len=%d, want 1", len(fw.PrivateSubnets))
	}
	ps := fw.PrivateSubnets[0]
	if ps.Id != "psaaaaaaaaaaaaaaaaaaaaaaaa" || ps.Name != "tf-acc-ps-a" || ps.State != "available" || ps.Net4 != "10.0.0.0/26" || ps.Net6 != "fd00::/64" {
		t.Errorf("subnet scalar fields mismatch: %+v", ps)
	}
	if len(ps.Nics) != 1 || ps.Nics[0].PrivateIpv4 != "10.0.0.5" || ps.Nics[0].VmName == nil || *ps.Nics[0].VmName != "tf-acc-vm" {
		t.Errorf("subnet nics mismatch: %+v", ps.Nics)
	}
	if len(ps.Firewalls) != 1 || ps.Firewalls[0].Id != "fwfg7td83em22qfw9pq5xyfqb7" || len(ps.Firewalls[0].FirewallRules) != 1 {
		t.Errorf("subnet nested firewalls mismatch: %+v", ps.Firewalls)
	}
}
