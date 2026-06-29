package provider

import (
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_vm"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_vm"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// sampleVmResponse mimics the detailed VM serialization (Serializers::Vm with
// detailed: true) that both the create POST and the details GET return. id, name,
// state, size, unix_user, storage_size_gib, boot_image, ip6, ip4_enabled and ip4 are
// base fields; firewalls, private_ipv4, private_ipv6, subnet and gpu are detailed-only.
// init_script is never serialized.
func sampleVmResponse() ubicloud_client.Vm {
	gpu := "1x NVIDIA A100"
	ip4 := "1.2.3.4"
	ip6 := "fd00::1"
	return ubicloud_client.Vm{
		Id:             "vm0000000000000000000000aa",
		Name:           "tf-acc-vm",
		State:          "creating",
		Location:       "aws-us-east-1",
		Size:           "standard-2",
		UnixUser:       "ubi",
		StorageSizeGib: 40,
		BootImage:      "ubuntu-jammy",
		Ip4Enabled:     true,
		Ip4:            &ip4,
		Ip6:            &ip6,
		Gpu:            &gpu,
		PrivateIpv4:    "10.0.0.5",
		PrivateIpv6:    "fd00::5",
		Subnet:         "tf-acc-ps",
		Firewalls:      []ubicloud_client.Firewall{},
	}
}

// TestSetVmStateResource guards the read-back of the vm resource's computed surface
// (ip4_enabled and the base/detailed fields) and the write-only treatment of gpu. gpu is
// an Optional-only create input whose request form ("count:type") differs from the read
// display form ("Nx <device_name>"), so the mapper must never hydrate it from the response:
// an unconfigured create (or an import) leaves gpu null even though the response carries a
// display-form value. init_script is Optional-only and never serialized, so like gpu an
// unconfigured (null) input stays null and the mapper never hydrates it. Computed attributes
// are seeded unknown to mirror a vanilla create's planned state; after mapping none may
// remain unknown.
func TestSetVmStateResource(t *testing.T) {
	ctx := t.Context()
	vmd := sampleVmResponse() // carries gpu "1x NVIDIA A100"
	m := resource_vm.VmModel{
		Id:             types.StringUnknown(),
		Ip4Enabled:     types.BoolUnknown(),
		Gpu:            types.StringNull(),
		InitScript:     types.StringNull(),
		PrivateIpv4:    types.StringUnknown(),
		PrivateIpv6:    types.StringUnknown(),
		Size:           types.StringUnknown(),
		StorageSizeGib: types.Int64Unknown(),
		Subnet:         types.StringUnknown(),
		UnixUser:       types.StringUnknown(),
	}
	if diags := setVmStateResource(ctx, &vmd, &m); diags.HasError() {
		t.Fatalf("setVmStateResource diags: %+v", diags)
	}

	if m.Ip4Enabled.IsUnknown() || m.Ip4Enabled.IsNull() || !m.Ip4Enabled.ValueBool() {
		t.Errorf("ip4_enabled = %v, want known true", m.Ip4Enabled)
	}
	// gpu is write-only: the response carries a display-form gpu, but an unconfigured
	// (null) input must stay null, never picking up the divergent read form. This is also
	// the import case: an imported gpu vm leaves gpu null, and the canonical post-import
	// config omits gpu (the display form is surfaced by the data source instead).
	if !m.Gpu.IsNull() {
		t.Errorf("gpu = %q, want null (write-only input, never read back)", m.Gpu.ValueString())
	}
	// init_script is Optional-only and write-only: the response never carries it, so an
	// unconfigured (null) input stays null and the mapper never hydrates it (like gpu).
	if !m.InitScript.IsNull() {
		t.Errorf("init_script = %q, want null (write-only input, never read back)", m.InitScript.ValueString())
	}

	for name, v := range map[string]attr.Value{
		"id": m.Id, "name": m.Name, "location": m.Location, "ip4_enabled": m.Ip4Enabled,
		"init_script": m.InitScript, "private_ipv4": m.PrivateIpv4,
		"private_ipv6": m.PrivateIpv6, "size": m.Size, "storage_size_gib": m.StorageSizeGib,
		"subnet": m.Subnet, "unix_user": m.UnixUser, "firewalls": m.Firewalls,
	} {
		if v.IsUnknown() {
			t.Errorf("%s is unknown after mapping", name)
		}
	}
}

// TestSetVmStateResourceGpuConfigured proves a configured gpu input survives the mapping
// unchanged. gpu is Optional-only and write-only: its request form ("count:type") differs
// from the response display form, so the mapper must not clobber the plan value or apply
// fails with an inconsistent result. The response carries the divergent display form, which
// must be ignored.
func TestSetVmStateResourceGpuConfigured(t *testing.T) {
	ctx := t.Context()
	vmd := sampleVmResponse() // carries gpu "1x NVIDIA A100"
	m := resource_vm.VmModel{Ip4Enabled: types.BoolUnknown(), Gpu: types.StringValue("1:a100"), InitScript: types.StringNull()}
	if diags := setVmStateResource(ctx, &vmd, &m); diags.HasError() {
		t.Fatalf("setVmStateResource diags: %+v", diags)
	}

	if m.Gpu.ValueString() != "1:a100" {
		t.Errorf("gpu = %q, want the configured value 1:a100 preserved", m.Gpu.ValueString())
	}
}

// TestSetVmStateResourceInitScriptConfigured guards that a configured init_script survives
// mapping unchanged. init_script is Optional-only and write-only: the response never carries
// it, so the mapper must never touch the plan value or apply fails with an inconsistent
// result. Mirrors TestSetVmStateResourceGpuConfigured for the other write-only input.
func TestSetVmStateResourceInitScriptConfigured(t *testing.T) {
	ctx := t.Context()
	vmd := sampleVmResponse()
	script := "#!/bin/bash\necho hello"
	m := resource_vm.VmModel{Ip4Enabled: types.BoolUnknown(), Gpu: types.StringNull(), InitScript: types.StringValue(script)}
	if diags := setVmStateResource(ctx, &vmd, &m); diags.HasError() {
		t.Fatalf("setVmStateResource diags: %+v", diags)
	}

	if m.InitScript.ValueString() != script {
		t.Errorf("init_script = %q, want the configured value preserved", m.InitScript.ValueString())
	}
}

// TestSetVmStateDatasource guards the same computed surface on the data source, whose
// Read fetches the detailed response. ip4_enabled, gpu and boot_image were dropped and
// now map from the response.
func TestSetVmStateDatasource(t *testing.T) {
	ctx := t.Context()
	vmd := sampleVmResponse()
	m := datasource_vm.VmModel{
		Ip4Enabled: types.BoolUnknown(),
		Gpu:        types.StringUnknown(),
		BootImage:  types.StringUnknown(),
	}
	if diags := setVmStateDatasource(ctx, &vmd, &m); diags.HasError() {
		t.Fatalf("setVmStateDatasource diags: %+v", diags)
	}

	if m.Ip4Enabled.IsUnknown() || m.Ip4Enabled.IsNull() || !m.Ip4Enabled.ValueBool() {
		t.Errorf("ip4_enabled = %v, want known true", m.Ip4Enabled)
	}
	if m.Gpu.ValueString() != "1x NVIDIA A100" {
		t.Errorf("gpu = %q, want the response value", m.Gpu.ValueString())
	}
	if m.BootImage.ValueString() != "ubuntu-jammy" {
		t.Errorf("boot_image = %q, want the response value", m.BootImage.ValueString())
	}

	for name, v := range map[string]attr.Value{
		"id": m.Id, "name": m.Name, "location": m.Location, "state": m.State,
		"ip4": m.Ip4, "ip6": m.Ip6, "ip4_enabled": m.Ip4Enabled, "gpu": m.Gpu,
		"boot_image": m.BootImage, "size": m.Size, "unix_user": m.UnixUser,
		"storage_size_gib": m.StorageSizeGib, "private_ipv4": m.PrivateIpv4,
		"private_ipv6": m.PrivateIpv6, "subnet": m.Subnet, "firewalls": m.Firewalls,
	} {
		if v.IsUnknown() {
			t.Errorf("data source %s is unknown after mapping", name)
		}
	}
}
