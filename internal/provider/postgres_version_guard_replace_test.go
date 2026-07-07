package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
)

// A replace plan destroys and recreates at the planned version, so a downgrade is a legal
// fresh create, not an in-place downgrade.
func TestModifyPlanVersionDowngradeWithReplaceAllowed(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17"), "private_subnet_name": strRaw("tf-acc-net-a")},
		map[string]tftypes.Value{"version": strRaw("16"), "private_subnet_name": strRaw("tf-acc-net-b")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("version downgrade on a replace plan must be allowed: %+v", resp.Diagnostics)
	}
}

func TestModifyPlanVersionMultiMajorWithReplaceAllowed(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16"), "flavor": strRaw("standard")},
		map[string]tftypes.Value{"version": strRaw("18"), "flavor": strRaw("paradedb")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("multi-major version jump on a replace plan must be allowed: %+v", resp.Diagnostics)
	}
}

func TestModifyPlanVersionWithSizeOnReplaceAllowed(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16"), "size": strRaw("m8gd.large"), "restrict_by_default": boolRaw(false)},
		map[string]tftypes.Value{"version": strRaw("17"), "size": strRaw("m8gd.xlarge"), "restrict_by_default": boolRaw(true)},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("version+size on a replace plan must be allowed: %+v", resp.Diagnostics)
	}
}

// A parent-set replace recreates through the read-replica endpoint, whose body omits
// version, so the planned version would be silently dropped; reject at plan.
func TestModifyPlanVersionWithParentReplaceRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("16"), "parent": strRaw("tf-acc-src")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("version change combined with a parent replace must be rejected at plan time")
	}
}

func TestModifyPlanVersionUpgradeWithParentReplaceRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("18"), "parent": strRaw("tf-acc-src")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("one-major upgrade combined with a parent replace must be rejected at plan time")
	}
}

func TestModifyPlanVersionWithRestoreTargetReplaceRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("16"), "parent": strRaw("tf-acc-src"), "restore_target": strRaw("2026-01-01T00:00:00Z")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("version change combined with a restore_target replace must be rejected at plan time")
	}
}

// An unknown planned version would be dropped silently by the child create, past the
// framework's known-value inconsistency check, so it is rejected like a known change.
func TestModifyPlanVersionUnknownWithParentReplaceRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17")},
		map[string]tftypes.Value{"version": tftypes.NewValue(tftypes.String, tftypes.UnknownValue), "parent": strRaw("tf-acc-src")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("unknown version pinned against a known parent replace must be rejected at plan time")
	}
}

// An unknown parent still routes the recreate to the child create (a real source or a
// rejected blank, never a version-honoring primary), so the version change is rejected, not deferred.
func TestModifyPlanVersionWithUnknownParentReplaceRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("16"), "parent": tftypes.NewValue(tftypes.String, tftypes.UnknownValue)},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("version change combined with an unknown parent replace must be rejected at plan time")
	}
}

func TestModifyPlanVersionUnknownWithUnknownParentReplaceRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17")},
		map[string]tftypes.Value{"version": tftypes.NewValue(tftypes.String, tftypes.UnknownValue), "parent": tftypes.NewValue(tftypes.String, tftypes.UnknownValue)},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("unknown version plus unknown parent replace must be rejected at plan time")
	}
}

// An unknown (interpolated) immutable counts as a pending replace, matching
// stringplanmodifier.RequiresReplace.
func TestPostgresPlanIsReplace(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(plan, state *resource_postgres.PostgresModel)
		want   bool
	}{
		{"no change", func(_, _ *resource_postgres.PostgresModel) {}, false},
		{"flavor", func(p, s *resource_postgres.PostgresModel) {
			s.Flavor, p.Flavor = types.StringValue("standard"), types.StringValue("paradedb")
		}, true},
		{"parent", func(p, s *resource_postgres.PostgresModel) {
			s.Parent, p.Parent = types.StringValue("tf-acc-src-a"), types.StringValue("tf-acc-src-b")
		}, true},
		{"restore_target", func(p, s *resource_postgres.PostgresModel) {
			s.RestoreTarget, p.RestoreTarget = types.StringNull(), types.StringValue("2026-01-01T00:00:00Z")
		}, true},
		{"restrict_by_default", func(p, s *resource_postgres.PostgresModel) {
			s.RestrictByDefault, p.RestrictByDefault = types.BoolValue(false), types.BoolValue(true)
		}, true},
		{"private_subnet_name", func(p, s *resource_postgres.PostgresModel) {
			s.PrivateSubnetName, p.PrivateSubnetName = types.StringValue("net-a"), types.StringValue("net-b")
		}, true},
		{"project_id", func(p, s *resource_postgres.PostgresModel) {
			s.ProjectId, p.ProjectId = types.StringValue("pj-a"), types.StringValue("pj-b")
		}, true},
		{"location", func(p, s *resource_postgres.PostgresModel) {
			s.Location, p.Location = types.StringValue("aws-us-east-1"), types.StringValue("aws-us-west-2")
		}, true},
		{"unknown private_subnet_name defers as replace", func(p, s *resource_postgres.PostgresModel) {
			s.PrivateSubnetName, p.PrivateSubnetName = types.StringValue("net-a"), types.StringUnknown()
		}, true},
		{"mutable size change is not a replace", func(p, s *resource_postgres.PostgresModel) {
			s.Size, p.Size = types.StringValue("m8gd.large"), types.StringValue("m8gd.xlarge")
		}, false},
		{"version change is not a replace", func(p, s *resource_postgres.PostgresModel) {
			s.Version, p.Version = types.StringValue("16"), types.StringValue("17")
		}, false},
	}
	for _, c := range cases {
		var plan, state resource_postgres.PostgresModel
		c.mutate(&plan, &state)
		if got := postgresPlanIsReplace(&plan, &state); got != c.want {
			t.Errorf("%s: postgresPlanIsReplace = %v, want %v", c.name, got, c.want)
		}
	}
}
