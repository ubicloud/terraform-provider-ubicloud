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
