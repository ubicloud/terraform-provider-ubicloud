package provider

import (
	"context"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A KNOWN plan value that differs from state; an unpinned (unknown) computed never trips.
func postgresAttrChanged(planVal, stateVal attr.Value) bool {
	return !planVal.IsUnknown() && !planVal.Equal(stateVal)
}

// Reproduces the framework's replace decision, which a resource ModifyPlan cannot read (the
// response's RequiresReplace starts empty), with the same comparison the attribute modifiers
// make: a RequiresReplace attr whose planned value differs from prior, unknown included.
func postgresPlanIsReplace(plan, state *resource_postgres.PostgresModel) bool {
	return !plan.Flavor.Equal(state.Flavor) ||
		!plan.Parent.Equal(state.Parent) ||
		!plan.RestoreTarget.Equal(state.RestoreTarget) ||
		!plan.RestrictByDefault.Equal(state.RestrictByDefault) ||
		!plan.PrivateSubnetName.Equal(state.PrivateSubnetName) ||
		!plan.ProjectId.Equal(state.ProjectId) ||
		!plan.Location.Equal(state.Location)
}

// Overlays the config read-back onto state; nil is a no-op. Update holds plan values instead.
func applyPostgresConfigToState(ctx context.Context, cfg *ubicloud_client.PostgresConfig, state *resource_postgres.PostgresModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if cfg == nil {
		return diags
	}
	pgConfig, d := types.MapValueFrom(ctx, types.StringType, cfg.PgConfig)
	diags.Append(d...)
	pgbouncerConfig, d := types.MapValueFrom(ctx, types.StringType, cfg.PgbouncerConfig)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}
	state.PgConfig = pgConfig
	state.PgbouncerConfig = pgbouncerConfig
	return diags
}
