package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
)

func boolRaw(b bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, b) }

func rawFirewallRules(t *testing.T, ctx context.Context, rules []map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	objType := postgresResourceSchemaObjType(t, ctx)
	listType, ok := objType.AttributeTypes["firewall_rules"].(tftypes.List)
	if !ok {
		t.Fatalf("firewall_rules attr type is not a list: %T", objType.AttributeTypes["firewall_rules"])
	}
	elemType := listType.ElementType
	elems := make([]tftypes.Value, 0, len(rules))
	for _, r := range rules {
		elems = append(elems, tftypes.NewValue(elemType, r))
	}
	return tftypes.NewValue(listType, elems)
}

// driveModifyPlanConfig takes SEPARATE config and plan values (driveModifyPlan wires
// Config == Plan), so a test can express config-null-but-plan-pinned shapes like the phantom.
func driveModifyPlanConfig(t *testing.T, ctx context.Context, configOver, stateOver, planOver map[string]tftypes.Value) *resource.ModifyPlanResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	configRaw := mkPGRaw(t, ctx, configOver)
	stateRaw := mkPGRaw(t, ctx, stateOver)
	planRaw := mkPGRaw(t, ctx, planOver)
	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: schema, Raw: configRaw},
		State:  tfsdk.State{Schema: schema, Raw: stateRaw},
		Plan:   tfsdk.Plan{Schema: schema, Raw: planRaw},
	}
	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: schema, Raw: planRaw}}
	(&postgresResource{}).ModifyPlan(ctx, req, resp)
	return resp
}

// driveModifyPlanCreate runs ModifyPlan on the create path: a null prior State (the create
// signal) and a bare postgresRaw Config, with no Plan seeded.
func driveModifyPlanCreate(t *testing.T, ctx context.Context, configOver map[string]tftypes.Value) *resource.ModifyPlanResponse {
	t.Helper()
	schema := resource_postgres.PostgresResourceSchema(ctx)
	objType := postgresResourceSchemaObjType(t, ctx)
	req := resource.ModifyPlanRequest{
		State:  tfsdk.State{Schema: schema, Raw: tftypes.NewValue(objType, nil)},
		Config: tfsdk.Config{Schema: schema, Raw: postgresRaw(t, ctx, configOver)},
	}
	resp := &resource.ModifyPlanResponse{}
	(&postgresResource{}).ModifyPlan(ctx, req, resp)
	return resp
}

// Prior state holds server tags while config omits them (post-import or un-managed); the
// plan pins USFU attributes to prior and leaves unpinned computeds unknown, as the framework would.
func importedConfigNullTagsFixture(t *testing.T, ctx context.Context) (stateOver, planOver map[string]tftypes.Value) {
	t.Helper()
	pinned := map[string]tftypes.Value{
		"id":                          strRaw("pg0test0001"),
		"size":                        strRaw("m8gd.large"),
		"storage_size":                numRaw(64),
		"ha_type":                     strRaw("none"),
		"version":                     strRaw("17"),
		"flavor":                      strRaw("standard"),
		"tags":                        rawTags(t, ctx, [][2]string{{"team", "data"}}),
		"pg_config":                   rawConfigMap(map[string]string{}),
		"pgbouncer_config":            rawConfigMap(map[string]string{}),
		"ca_certificates":             strRaw("-----BEGIN CERTIFICATE-----"),
		"created_at":                  strRaw("2026-07-01T00:00:00Z"),
		"maintenance_window_start_at": numRaw(3),
		"primary":                     boolRaw(true),
		"read_replica":                boolRaw(false),
		"username":                    strRaw("postgres"),
	}
	cascade := map[string]tftypes.Value{
		"state":                   strRaw("running"),
		"vm_size":                 strRaw("m8gd.large"),
		"storage_size_gib":        numRaw(64),
		"target_vm_size":          strRaw("m8gd.large"),
		"target_storage_size_gib": numRaw(64),
		"target_version":          strRaw("17"),
		"target_server_count":     numRaw(1),
		"connection_string":       strRaw("postgres://postgres:supersecret@pg.example.com:5432/postgres"),
		"hostname":                strRaw("pg.example.com"),
		"password":                strRaw("supersecret"),
		"earliest_restore_time":   strRaw("2026-07-02T00:00:00Z"),
		"latest_restore_time":     strRaw("2026-07-02T01:00:00Z"),
		"fallback_active":         boolRaw(false),
		"firewall_rules": rawFirewallRules(t, ctx, []map[string]tftypes.Value{{
			"cidr":        strRaw("0.0.0.0/0"),
			"description": strRaw("default"),
			"id":          strRaw("fw0000000001"),
			"port":        numRaw(5432),
		}}),
	}
	stateOver = map[string]tftypes.Value{}
	for k, v := range pinned {
		stateOver[k] = v
	}
	for k, v := range cascade {
		stateOver[k] = v
	}
	planOver = withUnknownComputeds(t, ctx, pinned)
	return stateOver, planOver
}

// Core's no-op gate compares proposed state (config-null tags = null) to the hydrated prior
// BEFORE plan modifiers run and cascades; ModifyPlan re-pins so the plan equals prior exactly.
func TestModifyPlanAbsorbsConfigNullTagsPhantom(t *testing.T) {
	ctx := t.Context()
	stateOver, planOver := importedConfigNullTagsFixture(t, ctx)
	config := map[string]tftypes.Value{
		"size":         strRaw("m8gd.large"),
		"storage_size": numRaw(64),
		"ha_type":      strRaw("none"),
		"version":      strRaw("17"),
		// tags omitted => null in config: the phantom trigger.
	}
	resp := driveModifyPlanConfig(t, ctx, config, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if !resp.Plan.Raw.IsFullyKnown() {
		t.Errorf("plan still carries unknown computeds (phantom not absorbed): %v", resp.Plan.Raw)
	}
	stateRaw := mkPGRaw(t, ctx, stateOver)
	if !resp.Plan.Raw.Equal(stateRaw) {
		t.Errorf("plan does not equal prior state after re-pin (Terraform would still plan a change):\n plan=%v\nstate=%v", resp.Plan.Raw, stateRaw)
	}
}

func TestModifyPlanAbsorbsConfigNullTagsPhantomMinimalConfig(t *testing.T) {
	ctx := t.Context()
	stateOver, planOver := importedConfigNullTagsFixture(t, ctx)
	resp := driveModifyPlanConfig(t, ctx, map[string]tftypes.Value{}, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if !resp.Plan.Raw.IsFullyKnown() {
		t.Errorf("plan still carries unknown computeds (phantom not absorbed): %v", resp.Plan.Raw)
	}
	stateRaw := mkPGRaw(t, ctx, stateOver)
	if !resp.Plan.Raw.Equal(stateRaw) {
		t.Errorf("plan does not equal prior state after re-pin:\n plan=%v\nstate=%v", resp.Plan.Raw, stateRaw)
	}
}

func TestModifyPlanRealSizeChangeNotAbsorbed(t *testing.T) {
	ctx := t.Context()
	stateOver, planOver := importedConfigNullTagsFixture(t, ctx)
	planOver["size"] = strRaw("m8gd.xlarge")
	config := map[string]tftypes.Value{
		"size": strRaw("m8gd.xlarge"), // a real resize, tags still omitted
	}
	resp := driveModifyPlanConfig(t, ctx, config, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.Plan.Get(ctx, &out); diags.HasError() {
		t.Fatalf("plan get: %+v", diags)
	}
	if !out.TargetVmSize.IsUnknown() {
		t.Errorf("target_vm_size = %v, want unknown (a real resize must not be absorbed)", out.TargetVmSize)
	}
	if !out.VmSize.IsUnknown() {
		t.Errorf("vm_size = %v, want unknown (a real resize must not be absorbed)", out.VmSize)
	}
}

func TestModifyPlanExplicitTagsChangeNotAbsorbed(t *testing.T) {
	ctx := t.Context()
	stateOver, planOver := importedConfigNullTagsFixture(t, ctx)
	planOver["tags"] = rawTags(t, ctx, [][2]string{{"team", "data"}, {"env", "prod"}})
	config := map[string]tftypes.Value{
		"tags": rawTags(t, ctx, [][2]string{{"team", "data"}, {"env", "prod"}}),
	}
	resp := driveModifyPlanConfig(t, ctx, config, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.Plan.Get(ctx, &out); diags.HasError() {
		t.Fatalf("plan get: %+v", diags)
	}
	if !out.ConnectionString.IsUnknown() {
		t.Errorf("connection_string = %v, want unknown (an explicit tags change must not be absorbed)", out.ConnectionString)
	}
}

// UseStateForUnknown does not pin a config-unknown attribute, so a pending interpolated
// resize plans size unknown; unknown compares unequal to the known prior and is not absorbed.
func TestModifyPlanUnknownConfigSizeNotAbsorbed(t *testing.T) {
	ctx := t.Context()
	stateOver, planOver := importedConfigNullTagsFixture(t, ctx)
	planOver["size"] = tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	config := map[string]tftypes.Value{
		"size": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	}
	resp := driveModifyPlanConfig(t, ctx, config, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.Plan.Get(ctx, &out); diags.HasError() {
		t.Fatalf("plan get: %+v", diags)
	}
	if !out.TargetVmSize.IsUnknown() {
		t.Errorf("target_vm_size = %v, want unknown (an unresolved interpolated size must not be absorbed)", out.TargetVmSize)
	}
}

// resp.RequiresReplace is empty inside a resource ModifyPlan (attribute-level RequiresReplace
// is collected separately), so the gate detects a replace by comparing immutables against prior.
func TestModifyPlanImmutableChangeNotAbsorbed(t *testing.T) {
	ctx := t.Context()
	stateOver, planOver := importedConfigNullTagsFixture(t, ctx)
	planOver["flavor"] = strRaw("paradedb")
	config := map[string]tftypes.Value{
		"flavor": strRaw("paradedb"),
	}
	resp := driveModifyPlanConfig(t, ctx, config, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.Plan.Get(ctx, &out); diags.HasError() {
		t.Fatalf("plan get: %+v", diags)
	}
	if !out.ConnectionString.IsUnknown() {
		t.Errorf("connection_string = %v, want unknown (a replace must not be absorbed)", out.ConnectionString)
	}
}

// Dropping create-only private_subnet_name plans a replace with plan null vs non-null prior;
// a config-vs-state check would read the null as "no change" and wrongly absorb the replace.
func TestModifyPlanWriteOnlyFieldRemovalNotAbsorbed(t *testing.T) {
	ctx := t.Context()
	stateOver, planOver := importedConfigNullTagsFixture(t, ctx)
	stateOver["private_subnet_name"] = strRaw("tf-acc-pg-ps")
	planOver["private_subnet_name"] = tftypes.NewValue(tftypes.String, nil)
	resp := driveModifyPlanConfig(t, ctx, map[string]tftypes.Value{}, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	var out resource_postgres.PostgresModel
	if diags := resp.Plan.Get(ctx, &out); diags.HasError() {
		t.Fatalf("plan get: %+v", diags)
	}
	if !out.ConnectionString.IsUnknown() {
		t.Errorf("connection_string = %v, want unknown (removing a create-only field is a replace, not the phantom)", out.ConnectionString)
	}
}

// ParentRefStability pins the planned parent to the prior canonical path when both name the
// same parent, so a replica's bare-name config still absorbs the phantom like a primary.
func TestModifyPlanReplicaParentNormalizedAbsorbed(t *testing.T) {
	ctx := t.Context()
	stateOver, planOver := importedConfigNullTagsFixture(t, ctx)
	parentPath := "/location/aws-us-east-1/postgres/tf-acc-pgsrc"
	stateOver["parent"] = strRaw(parentPath)
	stateOver["read_replica"] = boolRaw(true)
	stateOver["primary"] = boolRaw(false)
	planOver["parent"] = strRaw(parentPath)
	planOver["read_replica"] = boolRaw(true)
	planOver["primary"] = boolRaw(false)
	config := map[string]tftypes.Value{
		"parent": strRaw("tf-acc-pgsrc"), // bare name, as the user configures it
	}
	resp := driveModifyPlanConfig(t, ctx, config, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	if !resp.Plan.Raw.IsFullyKnown() {
		t.Errorf("replica plan still carries unknown computeds (phantom not absorbed): %v", resp.Plan.Raw)
	}
	stateRaw := mkPGRaw(t, ctx, stateOver)
	if !resp.Plan.Raw.Equal(stateRaw) {
		t.Errorf("replica plan does not equal prior state after re-pin:\n plan=%v\nstate=%v", resp.Plan.Raw, stateRaw)
	}
}

// A created config-null resource persists tags=null, so proposed null equals the null prior,
// the gate never trips, and ModifyPlan must leave the already-clean plan intact.
func TestModifyPlanCreatedConfigNullTagsStaysClean(t *testing.T) {
	ctx := t.Context()
	stateOver, _ := importedConfigNullTagsFixture(t, ctx)
	stateOver["tags"] = postgresTagsNullRaw(t, ctx) // created case persisted tags=null
	planOver := map[string]tftypes.Value{}
	for k, v := range stateOver {
		planOver[k] = v
	}
	resp := driveModifyPlanConfig(t, ctx, map[string]tftypes.Value{}, stateOver, planOver)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diags: %+v", resp.Diagnostics)
	}
	stateRaw := mkPGRaw(t, ctx, stateOver)
	if !resp.Plan.Raw.Equal(stateRaw) {
		t.Errorf("clean created-case plan was disturbed:\n plan=%v\nstate=%v", resp.Plan.Raw, stateRaw)
	}
}

func postgresTagsNullRaw(t *testing.T, ctx context.Context) tftypes.Value {
	t.Helper()
	objType := postgresResourceSchemaObjType(t, ctx)
	return tftypes.NewValue(objType.AttributeTypes["tags"], nil)
}
