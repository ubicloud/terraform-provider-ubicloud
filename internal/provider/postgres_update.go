package provider

import (
	"context"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

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
