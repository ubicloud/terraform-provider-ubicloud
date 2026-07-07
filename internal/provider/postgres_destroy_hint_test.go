package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// pgJoinDetails concatenates every diagnostic's summary and detail so a test can assert the
// user-visible guard text.
func pgJoinDetails(diags diag.Diagnostics) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Summary())
		b.WriteByte('\n')
		b.WriteString(d.Detail())
		b.WriteByte('\n')
	}
	return b.String()
}

// assertStaleConfigHint asserts an update-branch plan guard names the destroy/stale-config case
// and both escapes: realign the configuration, or run with -refresh=false.
func assertStaleConfigHint(t *testing.T, detail string) {
	t.Helper()
	for _, want := range []string{"terraform destroy", "-refresh=false"} {
		if !strings.Contains(detail, want) {
			t.Errorf("guard detail missing %q; got:\n%s", want, detail)
		}
	}
}

// A rejected in-place version downgrade blocks the destroy pre-walk, so its detail must name the
// destroy escapes and echo the current server version for a copy-paste realignment.
func TestModifyPlanVersionDowngradeHintNamesDestroyEscapes(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("18")},
		map[string]tftypes.Value{"version": strRaw("16")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("version downgrade must be rejected at plan time")
	}
	detail := pgJoinDetails(resp.Diagnostics)
	assertStaleConfigHint(t, detail)
	if !strings.Contains(detail, `version = "18"`) {
		t.Errorf("version guard must print the current server version; got:\n%s", detail)
	}
}

// A version upgrade combined with another change is rejected; the detail must carry the destroy
// escapes and the current server version.
func TestModifyPlanVersionCombinedHintNamesDestroyEscapes(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("17"), "size": strRaw("m8gd.large")},
		map[string]tftypes.Value{"version": strRaw("18"), "size": strRaw("m8gd.xlarge")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("version+size must be rejected at plan time")
	}
	detail := pgJoinDetails(resp.Diagnostics)
	assertStaleConfigHint(t, detail)
	if !strings.Contains(detail, `version = "17"`) {
		t.Errorf("combined-version guard must print the current server version; got:\n%s", detail)
	}
}

// The blank-parent update guard blocks the destroy pre-walk under a stale config, so it must name
// the destroy escapes too.
func TestModifyPlanBlankParentUpdateHintNamesDestroyEscapes(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"parent": strRaw("tf-acc-src")},
		map[string]tftypes.Value{"parent": strRaw("")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("blank parent must be rejected at plan time")
	}
	detail := pgJoinDetails(resp.Diagnostics)
	assertStaleConfigHint(t, detail)
	if !strings.Contains(detail, "omit parent") {
		t.Errorf("blank-parent guard must advise omitting parent; got:\n%s", detail)
	}
}

// A tags change on a read replica is rejected; the detail must name the destroy escapes.
func TestModifyPlanReplicaTagsHintNamesDestroyEscapes(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"read_replica": boolRaw(true), "tags": rawTags(t, ctx, [][2]string{{"team", "data"}})},
		map[string]tftypes.Value{"tags": rawTags(t, ctx, [][2]string{{"team", "prod"}})},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("tags change on a read replica must be rejected at plan time")
	}
	detail := pgJoinDetails(resp.Diagnostics)
	assertStaleConfigHint(t, detail)
	if !strings.Contains(detail, "restore tags to their current server values") {
		t.Errorf("read-replica tags guard must advise restoring tags to server values; got:\n%s", detail)
	}
}

// A source-inherited attr on a read replica is rejected; the detail must name the destroy escapes.
func TestModifyPlanReplicaInheritedHintNamesDestroyEscapes(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"read_replica": boolRaw(true)},
		map[string]tftypes.Value{"size": strRaw("m8gd.xlarge")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("size on a read replica must be rejected at plan time")
	}
	detail := pgJoinDetails(resp.Diagnostics)
	assertStaleConfigHint(t, detail)
	if !strings.Contains(detail, "remove size from the configuration") {
		t.Errorf("read-replica inherited-attr guard must advise removing the argument; got:\n%s", detail)
	}
}

// A source-inherited attr set alongside a parent-set replace is rejected; the detail must name the
// destroy escapes.
func TestModifyPlanParentReplaceRejectHintNamesDestroyEscapes(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"parent": strRaw("tf-acc-src-a"), "size": strRaw("m8gd.large")},
		map[string]tftypes.Value{"parent": strRaw("tf-acc-src-b"), "size": strRaw("m8gd.xlarge")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("inherited attr on a parent-set replace must be rejected at plan time")
	}
	detail := pgJoinDetails(resp.Diagnostics)
	assertStaleConfigHint(t, detail)
	if !strings.Contains(detail, "remove size from the configuration") {
		t.Errorf("parent-replace reject guard must advise removing the argument; got:\n%s", detail)
	}
}

// A config pinned to the lagging version during a pending upgrade blocks the destroy pre-walk,
// so its detail must name the destroy escapes and the pending target for a copy-paste realignment.
func TestModifyPlanInflightUpgradeHintNamesDestroyEscapes(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{"version": strRaw("16"), "target_version": strRaw("17")},
		map[string]tftypes.Value{"version": strRaw("16")},
	)
	if !resp.Diagnostics.HasError() {
		t.Fatal("stale config during a pending upgrade must be rejected at plan time")
	}
	detail := pgJoinDetails(resp.Diagnostics)
	assertStaleConfigHint(t, detail)
	if !strings.Contains(detail, `version = "17"`) {
		t.Errorf("pending-upgrade guard must print the target version for realignment; got:\n%s", detail)
	}
}
