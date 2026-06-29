package provider

import (
	"strings"
	"testing"

	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_vm"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_vm"
)

// gpu is a write-only create-only input: Optional-only, its create form "count:type"
// differs from the display read form, and it is never read back into state. The vm resource
// has no in-place Update, so a changed gpu must force a replace rather than hit the
// unsupported Update. gpu therefore carries RequiresReplace, and no UseStateForUnknown
// (Optional-only, so it never goes unknown). This is the vm resource's first plan modifier;
// the remaining vm immutables are the systematic all-resources-plan-modifiers pass.
func TestVmResourceGpuRequiresReplace(t *testing.T) {
	ctx := t.Context()
	s := resource_vm.VmResourceSchema(ctx)
	attr, ok := s.Attributes["gpu"]
	if !ok {
		t.Fatal("attribute \"gpu\" not found in schema")
	}
	descs := postgresPlanModifierDescriptions(ctx, attr)
	if !descsContain(descs, descRequiresReplace) {
		t.Errorf("gpu: expected RequiresReplace plan modifier, got %v", descs)
	}
	if descsContain(descs, descUseStateForUnknown) {
		t.Errorf("gpu: expected NO UseStateForUnknown (write-only Optional-only), got %v", descs)
	}
}

// TestVmGpuSchemaClassification locks the load-bearing schema transition of the write-only
// change. The resource gpu must be Optional-only: were codegen to regress it to
// Optional+Computed, a vanilla create would plan gpu as unknown and, with the read-back
// dropped, setVmStateResource would leave it unknown, reviving the inconsistent-result
// failure this change fixes (the plan-modifier and mapper tests would still pass). The data
// source gpu stays Computed read-only (unchanged). The resource description records the
// write-only residual: an imported vm has gpu unset, so any later gpu config (including the
// degenerate no-gpu form "0:") plans a replace; the data source surfaces the GPU for reads.
func TestVmGpuSchemaClassification(t *testing.T) {
	ctx := t.Context()

	rgpu := resource_vm.VmResourceSchema(ctx).Attributes["gpu"]
	rattr, ok := rgpu.(rschema.StringAttribute)
	if !ok {
		t.Fatalf("resource gpu: expected rschema.StringAttribute, got %T", rgpu)
	}
	if !rattr.Optional || rattr.Computed {
		t.Errorf("resource gpu: want Optional-only (Optional=true Computed=false), got Optional=%v Computed=%v", rattr.Optional, rattr.Computed)
	}
	for _, want := range []string{"Write-only", "replacement", "data source"} {
		if !strings.Contains(rattr.MarkdownDescription, want) {
			t.Errorf("resource gpu MarkdownDescription must document the write-only residual (missing %q); got %q", want, rattr.MarkdownDescription)
		}
	}

	dgpu := datasource_vm.VmDataSourceSchema(ctx).Attributes["gpu"]
	dattr, ok := dgpu.(dsschema.StringAttribute)
	if !ok {
		t.Fatalf("data source gpu: expected dsschema.StringAttribute, got %T", dgpu)
	}
	if !dattr.Computed || dattr.Optional {
		t.Errorf("data source gpu: want Computed read-only (Computed=true Optional=false), got Computed=%v Optional=%v", dattr.Computed, dattr.Optional)
	}
}

// The vm resource has no in-place Update (vmResource.Update hard-errors), so every
// create-only immutable must RequiresReplace or a changed value plans an unsupported
// in-place update and hard-errors. These are vm's immutables: path identity (project_id,
// location, name); the create-body inputs absent from any read-back (public_key,
// boot_image, private_subnet_id, enable_ip4, storage_size, gpu, init_script); and the two
// Optional+Computed inputs the server echoes back (size, unix_user).
func TestVmResourceRequiresReplace(t *testing.T) {
	ctx := t.Context()
	s := resource_vm.VmResourceSchema(ctx)
	for _, name := range []string{
		"project_id", "location", "name",
		"public_key", "boot_image", "private_subnet_id", "enable_ip4", "storage_size",
		"gpu", "init_script",
		"size", "unix_user",
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
// as "known after apply". The two Optional+Computed immutables (size, unix_user) also pin:
// the server echoes a non-null value, so UseStateForUnknown holds the omitted value to
// prior state and RequiresReplace then compares equal (no spurious replace). The issue body
// lists ip4/ip6, which do not exist on the resource schema (only ip4_enabled, private_ipv4,
// private_ipv6), so they are not asserted.
func TestVmResourceUseStateForUnknown(t *testing.T) {
	ctx := t.Context()
	s := resource_vm.VmResourceSchema(ctx)

	pinned := []string{
		"id", "ip4_enabled", "private_ipv4", "private_ipv6", "subnet",
		"storage_size_gib", "firewalls", "size", "unix_user",
	}
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

	// Write-only / Required immutables are never Computed, so they cannot go unknown and
	// must NOT carry UseStateForUnknown (a dead no-op); they take RequiresReplace alone,
	// asserted in TestVmResourceRequiresReplace.
	rrOnly := []string{
		"project_id", "location", "name",
		"public_key", "boot_image", "private_subnet_id", "enable_ip4", "storage_size",
		"gpu", "init_script",
	}
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

// size and unix_user are Optional+Computed read-back immutables: they carry BOTH
// UseStateForUnknown and RequiresReplace, and usfu must precede rr. The framework threads
// the planned value between modifiers in slice order, so pinning to prior state first lets
// RequiresReplace compare equal values on an omitted/no-op input and not force a spurious
// replace (mirrors postgres flavor).
func TestVmResourceModifierOrder(t *testing.T) {
	ctx := t.Context()
	s := resource_vm.VmResourceSchema(ctx)
	for _, name := range []string{"size", "unix_user"} {
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

// init_script is a write-only create-only input like gpu: its value is never read back
// (setVmStateResource never reads init_script back from the response), so it must be
// Optional-only, not Optional+Computed. Were it Computed, an omitted init_script would
// plan as unknown-after-apply over a null prior state that UseStateForUnknown cannot pin
// (usfu returns early on null state), so RequiresReplace would compare unknown != null and
// force a spurious replace on a no-op. Optional-only keeps the omitted value a known null,
// so RequiresReplace compares null == null and stays quiet. The data source has no
// init_script, so only the resource classification is asserted.
func TestVmInitScriptSchemaClassification(t *testing.T) {
	ctx := t.Context()
	attr := resource_vm.VmResourceSchema(ctx).Attributes["init_script"]
	sa, ok := attr.(rschema.StringAttribute)
	if !ok {
		t.Fatalf("resource init_script: expected rschema.StringAttribute, got %T", attr)
	}
	if !sa.Optional || sa.Computed {
		t.Errorf("resource init_script: want Optional-only (Optional=true Computed=false), got Optional=%v Computed=%v", sa.Optional, sa.Computed)
	}
	for _, want := range []string{"Write-only", "replacement"} {
		if !strings.Contains(sa.MarkdownDescription, want) {
			t.Errorf("resource init_script MarkdownDescription must document the write-only residual (missing %q); got %q", want, sa.MarkdownDescription)
		}
	}
}
