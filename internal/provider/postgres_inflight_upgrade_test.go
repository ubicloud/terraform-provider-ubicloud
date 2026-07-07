package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
)

// The server dispatched a 16->17 upgrade (version lags target_version), but the config still
// pins 16; Read maps the lagging 16 so the plan is a silent no-op that flips to a rejected
// downgrade once the server converges. Diagnose the mismatch while they still match.
func TestModifyPlanInflightUpgradeStaleConfigRejected(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("16")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a config pinned to the lagging version during a pending upgrade must be rejected at plan time")
	}
	detail := pgJoinDetails(resp.Diagnostics)
	// target_version != version is also the failed-upgrade shape, so the guard must not assert a
	// running upgrade; it describes the dispatched-but-incomplete state as running or failed.
	if strings.Contains(detail, "in flight") || !strings.Contains(detail, "running or failed") {
		t.Errorf("guard must describe the pending upgrade without asserting in-flight; got:\n%s", detail)
	}
	if !strings.Contains(detail, `version = "17"`) {
		t.Errorf("guard must point at the pending target version 17; got:\n%s", detail)
	}
}

// Config aligned to the in-flight target keeps planning the benign pending upgrade diff (16->17):
// the guard stands down and the version change survives for Update to reconcile as already-pending.
func TestModifyPlanInflightUpgradeAlignedConfigAllowed(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("config aligned to the in-flight target must plan the benign upgrade diff: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.Plan.Get(ctx, &out); diags.HasError() {
		t.Fatalf("plan get: %+v", diags)
	}
	if got := out.Version.ValueString(); got != "17" {
		t.Errorf("aligned plan must keep the pending version diff at 17, got %q", got)
	}
}

// A converged server carries target_version == version; the in-flight guard must never trip.
func TestModifyPlanConvergedVersionNoInflightGuard(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("17")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a converged server (target_version == version) must not trip the in-flight guard: %+v", resp.Diagnostics)
	}
}

// An omitted version self-heals through UseStateForUnknown (config null, plan pinned to prior),
// so it is never the stale-config trap. Keying on config, not the pinned plan, keeps it allowed.
func TestModifyPlanInflightUpgradeOmittedVersionAllowed(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlanConfig(t, ctx,
		map[string]tftypes.Value{},
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("16")},
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("an omitted version during an in-flight upgrade self-heals and must not be rejected: %+v", resp.Diagnostics)
	}
}
