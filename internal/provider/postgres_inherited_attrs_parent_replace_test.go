package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Path-based matching: the guard emits one error per attribute, and storage_size's detail
// contains size's, so a substring check cannot tell them apart.
func diagHasAttrError(diags diag.Diagnostics, p path.Path) bool {
	for _, d := range diags.Errors() {
		if wp, ok := d.(diag.DiagnosticWithPath); ok && wp.Path().Equal(p) {
			return true
		}
	}
	return false
}

// The child create omits size/storage_size and forces ha_type none; a known change trips
// the framework check only AFTER the destroy and an unknown one never does, so reject at plan.
func TestModifyPlanInheritedAttrChangeWithParentReplaceRejected(t *testing.T) {
	ctx := t.Context()
	unknownStr := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	unknownNum := tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
	src := strRaw("tf-acc-src")
	cases := []struct {
		name      string
		attr      string
		stateOver map[string]tftypes.Value
		planOver  map[string]tftypes.Value
	}{
		{"size known", "size",
			map[string]tftypes.Value{"size": strRaw("m8gd.large")},
			map[string]tftypes.Value{"size": strRaw("m8gd.xlarge"), "parent": src}},
		{"size unknown", "size",
			map[string]tftypes.Value{"size": strRaw("m8gd.large")},
			map[string]tftypes.Value{"size": unknownStr, "parent": src}},
		{"storage_size known", "storage_size",
			map[string]tftypes.Value{"storage_size": numRaw(64)},
			map[string]tftypes.Value{"storage_size": numRaw(128), "parent": src}},
		{"storage_size unknown", "storage_size",
			map[string]tftypes.Value{"storage_size": numRaw(64)},
			map[string]tftypes.Value{"storage_size": unknownNum, "parent": src}},
		{"ha_type known", "ha_type",
			map[string]tftypes.Value{"ha_type": strRaw("none")},
			map[string]tftypes.Value{"ha_type": strRaw("async"), "parent": src}},
		{"ha_type unknown", "ha_type",
			map[string]tftypes.Value{"ha_type": strRaw("none")},
			map[string]tftypes.Value{"ha_type": unknownStr, "parent": src}},
		{"size known on restore replace", "size",
			map[string]tftypes.Value{"size": strRaw("m8gd.large")},
			map[string]tftypes.Value{"size": strRaw("m8gd.xlarge"), "parent": src, "restore_target": strRaw("2026-01-01T00:00:00Z")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := driveModifyPlan(t, ctx, c.stateOver, c.planOver)
			if !diagHasAttrError(resp.Diagnostics, path.Root(c.attr)) {
				t.Fatalf("%s set alongside parent on a replace must be rejected at plan on the %s attribute: %+v", c.attr, c.attr, resp.Diagnostics)
			}
		})
	}
}

func TestModifyPlanInheritedAttrsAllReportedWithParentReplace(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"size": strRaw("m8gd.large"), "storage_size": numRaw(64), "ha_type": strRaw("none")},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge"), "storage_size": numRaw(128), "ha_type": strRaw("async"), "parent": strRaw("tf-acc-src")},
	)
	for _, name := range []string{"size", "storage_size", "ha_type"} {
		if !diagHasAttrError(resp.Diagnostics, path.Root(name)) {
			t.Errorf("%s change on a parent-set replace was not reported: %+v", name, resp.Diagnostics)
		}
	}
}

// The correct primary-to-replica conversion OMITS the inherited inputs; UseStateForUnknown
// pins them to prior, so they read equal and only a changed or unknown value is caught.
func TestModifyPlanPrimaryToReplicaOmittingInheritedAllowed(t *testing.T) {
	ctx := t.Context()
	stateOver := map[string]tftypes.Value{
		"read_replica": boolRaw(false),
		"primary":      boolRaw(true),
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
		"ha_type":      strRaw("none"),
		"version":      strRaw("17"),
	}
	planOver := map[string]tftypes.Value{
		"read_replica": boolRaw(false),
		"primary":      boolRaw(true),
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
		"ha_type":      strRaw("none"),
		"version":      strRaw("17"),
		"parent":       strRaw("tf-acc-src"),
	}
	config := map[string]tftypes.Value{"parent": strRaw("tf-acc-src")}
	resp := driveModifyPlanConfig(t, ctx, config, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("adding parent while omitting the inherited inputs is a valid primary-to-replica replace and must plan cleanly: %+v", resp.Diagnostics)
	}
}

// A re-restore (new restore_target) replaces through the restore create; with the inherited
// inputs unchanged the same source re-inherits them, so there is no drop to reject.
func TestModifyPlanRestoredPrimaryKeepInheritedOnReplaceAllowed(t *testing.T) {
	ctx := t.Context()
	src := strRaw("tf-acc-src")
	base := map[string]tftypes.Value{
		"read_replica": boolRaw(false),
		"parent":       src,
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
		"ha_type":      strRaw("none"),
		"version":      strRaw("17"),
	}
	stateOver := map[string]tftypes.Value{"restore_target": strRaw("2026-01-01T00:00:00Z")}
	planOver := map[string]tftypes.Value{"restore_target": strRaw("2026-02-01T00:00:00Z")}
	for k, v := range base {
		stateOver[k] = v
		planOver[k] = v
	}
	config := map[string]tftypes.Value{"parent": src, "restore_target": strRaw("2026-02-01T00:00:00Z")}
	resp := driveModifyPlanConfig(t, ctx, config, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a restore replace that keeps the inherited inputs unchanged must plan cleanly: %+v", resp.Diagnostics)
	}
}

func TestModifyPlanInheritedAttrChangeFreshPrimaryReplaceAllowed(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"size": strRaw("m8gd.large"), "private_subnet_name": strRaw("tf-acc-net-a")},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge"), "private_subnet_name": strRaw("tf-acc-net-b")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a size change on a parentless replace is a fresh recreate and must be allowed: %+v", resp.Diagnostics)
	}
}
