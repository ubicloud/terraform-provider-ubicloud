package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The read-replica guard arms share one summary, so rejection tests key on the detail.
func diagHasDetailContaining(diags diag.Diagnostics, substr string) bool {
	for _, d := range diags.Errors() {
		if strings.Contains(d.Detail(), substr) {
			return true
		}
	}
	return false
}

// A reparent is a destroy+recreate through POST .../read-replica, whose body accepts tags,
// so a tags change combined with a reparent is a legal replace, not a forbidden PATCH.
func TestModifyPlanReplicaReparentWithTagsChangeAllowed(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{
			"read_replica": boolRaw(true),
			"parent":       strRaw("tf-acc-src-a"),
			"tags":         rawTags(t, ctx, [][2]string{{"team", "data"}}),
		},
		map[string]tftypes.Value{
			"parent": strRaw("tf-acc-src-b"),
			"tags":   rawTags(t, ctx, [][2]string{{"team", "ops"}}),
		},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("reparent plus tags change on a read replica is a legal replace and must not be rejected at plan: %+v", resp.Diagnostics)
	}
}

// In place (same parent), a replica tags change is a PATCH the backend rejects.
func TestModifyPlanReplicaInPlaceTagsChangeRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{
			"read_replica": boolRaw(true),
			"parent":       strRaw("tf-acc-src-a"),
			"tags":         rawTags(t, ctx, [][2]string{{"team", "data"}}),
		},
		map[string]tftypes.Value{
			"parent": strRaw("tf-acc-src-a"),
			"tags":   rawTags(t, ctx, [][2]string{{"team", "ops"}}),
		},
	)
	if !diagHasDetailContaining(resp.Diagnostics, "tags cannot be changed on a read replica") {
		t.Fatalf("in-place tags change on a read replica must be rejected with the tags diagnostic: %+v", resp.Diagnostics)
	}
}

// The read-replica create body omits size/version, so the parent-inherited arm keeps
// rejecting them even on a replace, unlike tags which the replace frees.
func TestModifyPlanReplicaReparentWithInheritedAttrStillRejected(t *testing.T) {
	ctx := t.Context()
	state := map[string]tftypes.Value{"read_replica": boolRaw(true), "parent": strRaw("tf-acc-src-a")}
	for _, c := range []struct {
		name, detail string
		plan         map[string]tftypes.Value
	}{
		{"size", "size cannot be changed on a read replica", map[string]tftypes.Value{"parent": strRaw("tf-acc-src-b"), "size": strRaw("m8gd.large")}},
		{"version", "version cannot be changed on a read replica", map[string]tftypes.Value{"parent": strRaw("tf-acc-src-b"), "version": strRaw("17")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := driveModifyPlan(t, ctx, state, c.plan)
			if !diagHasDetailContaining(resp.Diagnostics, c.detail) {
				t.Fatalf("%s on a read-replica replace must still be rejected by the parent-inherited arm: %+v", c.name, resp.Diagnostics)
			}
		})
	}
}
