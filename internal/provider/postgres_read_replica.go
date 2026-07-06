package provider

import (
	"context"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A whitespace-only parent is treated as unset so it never routes to a meaningless path.
func postgresHasParent(state *resource_postgres.PostgresModel) bool {
	return !state.Parent.IsNull() && !state.Parent.IsUnknown() && strings.TrimSpace(state.Parent.ValueString()) != ""
}

// A KNOWN, present-but-blank reference (whitespace trims empty).
func postgresParentBlank(v types.String) bool {
	return !v.IsNull() && !v.IsUnknown() && strings.TrimSpace(v.ValueString()) == ""
}

// A known non-blank parent inherits size; an unknown parent or size defers to apply (the
// create guard backstops). A null parent stringifies to "", so the trimmed term subsumes it;
// size is trimmed so a whitespace-only size fails at plan, not opaquely at the server.
func postgresPrimaryMissingSize(parent, size types.String) bool {
	if parent.IsUnknown() || size.IsUnknown() || strings.TrimSpace(parent.ValueString()) != "" {
		return false
	}
	return size.IsNull() || strings.TrimSpace(size.ValueString()) == ""
}

// The endpoint accepts only {name, pg_config, pgbouncer_config, tags}; unset or unknown
// optionals are omitted so the request never serializes a placeholder.
func postgresReadReplicaBody(ctx context.Context, state *resource_postgres.PostgresModel) (ubicloud_client.CreatePostgresDatabaseReadReplicaJSONRequestBody, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := ubicloud_client.CreatePostgresDatabaseReadReplicaJSONRequestBody{
		Name: state.Name.ValueString(),
	}

	pgConfig, d := postgresConfigToBody(ctx, state.PgConfig)
	diags.Append(d...)
	body.PgConfig = pgConfig

	pgbouncerConfig, d := postgresConfigToBody(ctx, state.PgbouncerConfig)
	diags.Append(d...)
	body.PgbouncerConfig = pgbouncerConfig

	tags, d := postgresTagsToBody(ctx, state.Tags)
	diags.Append(d...)
	body.Tags = tags

	return body, diags
}

// Omits an unset or still-computed map.
func postgresConfigToBody(ctx context.Context, m types.Map) (*map[string]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if m.IsNull() || m.IsUnknown() {
		return nil, diags
	}
	out := make(map[string]string, len(m.Elements()))
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	if diags.HasError() {
		return nil, diags
	}
	return &out, diags
}

// Omits an unset or still-computed list.
func postgresTagsToBody(ctx context.Context, list types.List) (*[]ubicloud_client.PostgresTag, diag.Diagnostics) {
	var diags diag.Diagnostics
	if list.IsNull() || list.IsUnknown() {
		return nil, diags
	}
	var values []resource_postgres.TagsValue
	diags.Append(list.ElementsAs(ctx, &values, false)...)
	if diags.HasError() {
		return nil, diags
	}
	tags := make([]ubicloud_client.PostgresTag, 0, len(values))
	for _, v := range values {
		tags = append(tags, ubicloud_client.PostgresTag{Key: v.Key.ValueString(), Value: v.Value.ValueString()})
	}
	return &tags, diags
}
