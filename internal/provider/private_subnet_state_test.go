package provider

import (
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_private_subnet"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_private_subnet"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// samplePrivateSubnet mirrors a GetPrivateSubnetDetails response: the scalar fields
// (id, name, state, location, net4, net6) plus a nic. state carries whatever the
// backend reports; a freshly created subnet may be "creating" before it converges to
// "available", so the value is parameterized rather than fixed.
func samplePrivateSubnet(state string) ubicloud_client.PrivateSubnet {
	vmName := "tf-acc-vm"
	return ubicloud_client.PrivateSubnet{
		Id:        "psaaaaaaaaaaaaaaaaaaaaaaaa",
		Name:      "tf-acc-ps",
		State:     state,
		Location:  "aws-us-east-1",
		Net4:      "10.0.0.0/26",
		Net6:      "fd00::/64",
		Firewalls: []ubicloud_client.Firewall{},
		Nics: []ubicloud_client.Nic{
			{Id: "ncaaaaaaaaaaaaaaaaaaaaaaaa", Name: "tf-acc-nic", PrivateIpv4: "10.0.0.5", PrivateIpv6: "fd00::5", VmName: &vmName},
		},
	}
}

// TestSetPrivateSubnetStateResource guards the read-back of the private_subnet
// resource's computed state attribute (issue fix-private-subnet-resource-unmapped-state).
// state is Computed-only in the schema, so the plan the provider reads before Create/Read
// carries it as UNKNOWN; the mapper never wrote it, leaving it unknown after apply, which
// terraform rejects as "Provider returned invalid result object after apply". The model is
// seeded with the unknown to reproduce that path: after mapping, state must be known.
func TestSetPrivateSubnetStateResource(t *testing.T) {
	ctx := t.Context()
	ps := samplePrivateSubnet("creating")
	m := resource_private_subnet.PrivateSubnetModel{State: types.StringUnknown()}
	if diags := setPrivateSubnetStateResource(ctx, &ps, &m); diags.HasError() {
		t.Fatalf("setPrivateSubnetStateResource diags: %+v", diags)
	}

	if m.State.IsUnknown() || m.State.IsNull() {
		t.Fatalf("state unknown=%v null=%v, want a known value", m.State.IsUnknown(), m.State.IsNull())
	}
	if m.State.ValueString() != "creating" {
		t.Errorf("state = %q, want %q", m.State.ValueString(), "creating")
	}

	if m.Id.ValueString() != "psaaaaaaaaaaaaaaaaaaaaaaaa" || m.Net4.ValueString() != "10.0.0.0/26" || m.Net6.ValueString() != "fd00::/64" {
		t.Errorf("scalar fields mismatch: id=%q net4=%q net6=%q", m.Id.ValueString(), m.Net4.ValueString(), m.Net6.ValueString())
	}
	if m.Nics.IsUnknown() || m.Nics.IsNull() || len(m.Nics.Elements()) != 1 {
		t.Errorf("nics unknown=%v null=%v len=%d, want 1", m.Nics.IsUnknown(), m.Nics.IsNull(), len(m.Nics.Elements()))
	}
}

// TestSetPrivateSubnetStateDatasource guards the same state mapping on the data source.
// Its Read reads state from config, where the Computed attribute is absent (null); the
// mapper left it null, so the data source reported no state. After mapping, state must be
// the known value the response carried.
func TestSetPrivateSubnetStateDatasource(t *testing.T) {
	ctx := t.Context()
	ps := samplePrivateSubnet("available")
	var m datasource_private_subnet.PrivateSubnetModel
	if diags := setPrivateSubnetStateDatasource(ctx, &ps, &m); diags.HasError() {
		t.Fatalf("setPrivateSubnetStateDatasource diags: %+v", diags)
	}

	if m.State.IsUnknown() || m.State.IsNull() {
		t.Fatalf("state unknown=%v null=%v, want a known value", m.State.IsUnknown(), m.State.IsNull())
	}
	if m.State.ValueString() != "available" {
		t.Errorf("state = %q, want %q", m.State.ValueString(), "available")
	}
}
