package provider

import (
	"testing"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// restore_target is a create-only restore input (POST .../restore body), absent from the
// create body and every read response, so tfplugingen-openapi does not emit it; it is
// injected into the schema. It must be Optional (a normal create omits it), never Computed
// (nothing reads it back) and never Required (only a restore sets it).
func TestPostgresRestoreTargetSchema(t *testing.T) {
	ctx := t.Context()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	attr, ok := schema.Attributes["restore_target"]
	if !ok {
		t.Fatal("schema is missing the restore_target attribute")
	}
	if !attr.IsOptional() {
		t.Error("restore_target must be Optional")
	}
	if attr.IsComputed() {
		t.Error("restore_target must not be Computed (it is never read back)")
	}
	if attr.IsRequired() {
		t.Error("restore_target must not be Required (only a restore sets it)")
	}
}

// postgresHasRestoreTarget decides restore dispatch. Only null/unknown is "not a restore" (a
// normal create or a read replica leaves restore_target null). ANY known value is a restore
// attempt, including a blank one: restore_target is a timestamp, so a configured-but-blank
// value must route to the restore path and error there, never silently fall through to the
// read-replica branch. The RFC 3339 validator rejects a blank value at plan; this predicate
// is the dispatch decision, not the validity check.
func TestPostgresHasRestoreTarget(t *testing.T) {
	cases := []struct {
		name string
		rt   types.String
		want bool
	}{
		{"null", types.StringNull(), false},
		{"unknown", types.StringUnknown(), false},
		{"empty is a (malformed) restore attempt", types.StringValue(""), true},
		{"whitespace is a (malformed) restore attempt", types.StringValue("   "), true},
		{"timestamp", types.StringValue("2026-06-24T11:00:00Z"), true},
	}
	for _, c := range cases {
		var m resource_postgres.PostgresModel
		m.RestoreTarget = c.rt
		if got := postgresHasRestoreTarget(&m); got != c.want {
			t.Errorf("%s: postgresHasRestoreTarget = %v, want %v", c.name, got, c.want)
		}
	}
}

// ModifyPlan on CREATE must (finding 4) NOT emit the misleading primary-size error for a
// restore (restore_target inherits size from the source like a replica), and (finding 2)
// reject an explicitly blank parent when restore_target is set, before it becomes a
// POST .../postgres//restore at apply.
func TestPostgresModifyPlanRestore(t *testing.T) {
	ctx := t.Context()
	r := &postgresResource{}
	schema := resource_postgres.PostgresResourceSchema(ctx)
	objType := postgresResourceSchemaObjType(t, ctx)

	modifyCreate := func(overrides map[string]tftypes.Value) diag.Diagnostics {
		req := resource.ModifyPlanRequest{
			State:  tfsdk.State{Schema: schema, Raw: tftypes.NewValue(objType, nil)}, // null state = create
			Config: tfsdk.Config{Schema: schema, Raw: postgresRaw(t, ctx, overrides)},
		}
		resp := &resource.ModifyPlanResponse{}
		r.ModifyPlan(ctx, req, resp)
		return resp.Diagnostics
	}

	target := tftypes.NewValue(tftypes.String, "2026-06-24T11:00:00Z")
	// A valid restore (parent + restore_target, no size) is exempt from the primary-size guard.
	validRestore := map[string]tftypes.Value{
		"parent":         tftypes.NewValue(tftypes.String, "tf-acc-source"),
		"restore_target": target,
	}
	if d := modifyCreate(validRestore); d.HasError() {
		t.Errorf("valid restore (parent+restore_target, no size): unexpected error: %+v", d)
	}

	// restore_target with an explicitly blank parent is rejected at plan (blank source).
	blankParent := map[string]tftypes.Value{
		"parent":         tftypes.NewValue(tftypes.String, "   "),
		"restore_target": target,
	}
	if d := modifyCreate(blankParent); !d.HasError() {
		t.Error("restore_target with a blank parent: expected a plan-time error, got none")
	}

	// restore_target without parent (null): the size guard must NOT fire (gated off for a
	// restore), so ModifyPlan stays silent here and AlsoRequires(parent) -- a schema validator,
	// exercised in restore_local_check.sh -- is the single error the user sees.
	noParent := map[string]tftypes.Value{"restore_target": target}
	if d := modifyCreate(noParent); d.HasError() {
		t.Errorf("restore_target without parent: ModifyPlan must defer to AlsoRequires, not emit a size error: %+v", d)
	}

	// A restore whose target references a computed value (an already-backed-up source's
	// latest_restore_time) is unknown at plan; with parent set it must plan clean, with no size
	// demand and no misroute. At apply the value resolves to a known string and Create (reading
	// the resolved plan) routes to the restore path. A real timestamp restores; an empty value
	// (a source still creating reports latest_restore_time as "", see postgres_state_test.go)
	// fails closed on time.Parse with a clear diagnostic, never silently hitting the replica or
	// primary endpoint. Rejecting an unknown restore_target at plan would break this config, so
	// the plan stays silent and dispatch is correct once the value resolves.
	computedTarget := map[string]tftypes.Value{
		"parent":         tftypes.NewValue(tftypes.String, "tf-acc-source"),
		"restore_target": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	}
	if d := modifyCreate(computedTarget); d.HasError() {
		t.Errorf("computed (unknown) restore_target with parent: must plan clean: %+v", d)
	}
}

// The restore create body (POST .../restore) is the read-replica narrow set PLUS the
// required restore_target: {name, restore_target, pg_config, pgbouncer_config, tags}. None of
// the source-inherited create inputs (size/storage_size/version/ha_type/flavor) belong in it.
// restore_target is parsed from the user's RFC 3339 string into the time.Time the API expects;
// unset optional config/tags are omitted (nil), not sent as empty.
func TestPostgresRestoreBody(t *testing.T) {
	ctx := t.Context()
	var m resource_postgres.PostgresModel
	m.Name = types.StringValue("tf-acc-restored")
	m.RestoreTarget = types.StringValue("2026-06-24T11:00:00Z")
	m.PgConfig = mkStringMap(t, map[string]string{"max_connections": "100"})
	m.PgbouncerConfig = types.MapNull(types.StringType)
	m.Tags = mkResourceTags(t, [][2]string{{"team", "data"}})

	body, diags := postgresRestoreBody(ctx, &m)
	if diags.HasError() {
		t.Fatalf("postgresRestoreBody diags: %+v", diags)
	}

	if body.Name != "tf-acc-restored" {
		t.Errorf("name = %q, want tf-acc-restored", body.Name)
	}
	want, _ := time.Parse(time.RFC3339, "2026-06-24T11:00:00Z")
	if !body.RestoreTarget.Equal(want) {
		t.Errorf("restore_target = %v, want %v", body.RestoreTarget, want)
	}
	if body.PgConfig == nil || (*body.PgConfig)["max_connections"] != "100" {
		t.Errorf("pg_config = %v, want {max_connections:100}", body.PgConfig)
	}
	if body.PgbouncerConfig != nil {
		t.Errorf("pgbouncer_config = %v, want nil when unset", *body.PgbouncerConfig)
	}
	if body.Tags == nil || len(*body.Tags) != 1 {
		t.Fatalf("tags = %v, want one tag", body.Tags)
	}
	if (*body.Tags)[0].Key != "team" || (*body.Tags)[0].Value != "data" {
		t.Errorf("tag = {%q:%q}, want {team:data}", (*body.Tags)[0].Key, (*body.Tags)[0].Value)
	}
}

// A padded timestamp is trimmed before parsing so the API receives a clean RFC 3339 value,
// and unknown (computed-not-yet-resolved) config/tags are omitted: a restore created with
// only name+parent+restore_target must not serialize {} or an unknown placeholder.
func TestPostgresRestoreBodyOmitsUnknown(t *testing.T) {
	ctx := t.Context()
	var m resource_postgres.PostgresModel
	m.Name = types.StringValue("tf-acc-restored")
	m.RestoreTarget = types.StringValue("  2026-06-24T11:00:00Z  ")
	m.PgConfig = types.MapUnknown(types.StringType)
	m.PgbouncerConfig = types.MapUnknown(types.StringType)
	m.Tags = types.ListUnknown(resource_postgres.TagsValue{}.Type(ctx))

	body, diags := postgresRestoreBody(ctx, &m)
	if diags.HasError() {
		t.Fatalf("postgresRestoreBody diags: %+v", diags)
	}
	want, _ := time.Parse(time.RFC3339, "2026-06-24T11:00:00Z")
	if !body.RestoreTarget.Equal(want) {
		t.Errorf("restore_target = %v, want %v (padded input must be trimmed)", body.RestoreTarget, want)
	}
	if body.PgConfig != nil || body.PgbouncerConfig != nil || body.Tags != nil {
		t.Errorf("unknown config/tags must be omitted: pg=%v pgb=%v tags=%v", body.PgConfig, body.PgbouncerConfig, body.Tags)
	}
}

// A restore_target that is not a valid RFC 3339 timestamp is rejected before the request is
// sent, with a clear diagnostic rather than an opaque server error on a zero time.
func TestPostgresRestoreBodyInvalidTarget(t *testing.T) {
	ctx := t.Context()
	var m resource_postgres.PostgresModel
	m.Name = types.StringValue("tf-acc-restored")
	m.RestoreTarget = types.StringValue("not-a-timestamp")

	_, diags := postgresRestoreBody(ctx, &m)
	if !diags.HasError() {
		t.Error("expected a diagnostic for an invalid restore_target, got none")
	}
}

// restore_target is a write-only create input absent from every read response, so
// setPostgresStateResource must leave a user-supplied value untouched (terraform, not the
// server, owns it). A restored db's response carries parent (its source) and read_replica
// false; restore_target must survive the round trip unchanged.
func TestSetPostgresStateResourcePreservesRestoreTarget(t *testing.T) {
	ctx := t.Context()
	resp := sampleDetailedPostgresResponse()
	resp.ReadReplica = false
	resp.Parent = ptrTo("/location/aws-us-east-1/postgres/tf-acc-source")

	var m resource_postgres.PostgresModel
	m.RestoreTarget = types.StringValue("2026-06-24T11:00:00Z")

	if diags := setPostgresStateResource(ctx, &resp, &m); diags.HasError() {
		t.Fatalf("setPostgresStateResource diags: %+v", diags)
	}
	if got := m.RestoreTarget.ValueString(); got != "2026-06-24T11:00:00Z" {
		t.Errorf("restore_target = %q, want preserved user value (not touched by the read surface)", got)
	}
}
