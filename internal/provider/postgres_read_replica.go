package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
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

// The child create bodies accept none of the four (size/storage_size/version inherit from the
// source; ha_type is forced to none). Known values only; unknown defers to apply.
// flavor/private_subnet_name/restrict_by_default keep the static ConflictsWith(parent).
var postgresParentInheritedAttrs = []struct {
	name string
	get  func(*resource_postgres.PostgresModel) attr.Value
}{
	{"size", func(m *resource_postgres.PostgresModel) attr.Value { return m.Size }},
	{"storage_size", func(m *resource_postgres.PostgresModel) attr.Value { return m.StorageSize }},
	{"ha_type", func(m *resource_postgres.PostgresModel) attr.Value { return m.HaType }},
	{"version", func(m *resource_postgres.PostgresModel) attr.Value { return m.Version }},
}

func postgresParentInheritedError(name string) (summary, detail string) {
	return "Attribute cannot be set with parent",
		fmt.Sprintf("%s cannot be set when parent is specified; the read-replica and restore create bodies do not accept it, so omit it.", name)
}

// Wraps a guard's config-side fix with the destroy escape: terraform destroy defaults to
// -refresh=true, re-planning the configuration against refreshed state before deleting, so a
// drifted config blocks the destroy on these same guards. configFix reconciles this guard's
// argument; -refresh=false is the universal escape.
func postgresStaleConfigHint(configFix string) string {
	return fmt.Sprintf(" This also blocks terraform destroy, whose default -refresh=true re-plans "+
		"the configuration before deleting; to unblock the destroy, %s, or run terraform destroy "+
		"with -refresh=false.", configFix)
}

func postgresParentInheritedConfigured(config *resource_postgres.PostgresModel) []string {
	var names []string
	for _, a := range postgresParentInheritedAttrs {
		if v := a.get(config); !v.IsNull() && !v.IsUnknown() {
			names = append(names, a.name)
		}
	}
	return names
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

// parent is the reference (name or id) of an existing parent in the same project/location.
func (r *postgresResource) createPostgresReadReplica(ctx context.Context, state *resource_postgres.PostgresModel) (*ubicloud_client.PostgresDatabase, diag.Diagnostics) {
	body, diags := postgresReadReplicaBody(ctx, state)
	if diags.HasError() {
		return nil, diags
	}

	parent := strings.TrimSpace(state.Parent.ValueString())
	postgresResp, err := r.uc.client.CreatePostgresDatabaseReadReplicaWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), parent, body)
	if err != nil {
		diags.AddError(
			fmt.Sprintf("Error creating postgres read replica: %s (parent %s)", postgresResourceLogIdentifier(state), parent),
			err.Error(),
		)
		return nil, diags
	}

	if postgresResp.StatusCode() != http.StatusOK {
		diags.AddError(
			"Unexpected HTTP status code creating postgres read replica",
			fmt.Sprintf("Received %s for postgres read replica: %s (parent %s). Details: %s", postgresResp.Status(), postgresResourceLogIdentifier(state), parent, postgresResp.Body))
		return nil, diags
	}

	return postgresResp.JSON200, diags
}
