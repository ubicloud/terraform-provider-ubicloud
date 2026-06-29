package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// postgresHasParent reports whether the plan asks for a read replica: a known,
// non-blank parent reference. A normal create leaves parent unset, so the computed
// attribute is unknown (not a replica). A whitespace-only parent is treated as unset so
// it does not route to the read-replica endpoint as a meaningless path.
func postgresHasParent(state *resource_postgres.PostgresModel) bool {
	return !state.Parent.IsNull() && !state.Parent.IsUnknown() && strings.TrimSpace(state.Parent.ValueString()) != ""
}

// postgresPrimaryMissingSize reports whether a CREATE plan is a primary (no parent)
// that is missing its required size. A read replica (parent set) inherits size and is
// exempt. Unknown parent or size defers to apply: the value is an unresolved
// interpolation the framework fills in later, and createPostgresPrimary catches one
// that resolves empty. Only a known-absent size on a known-non-replica is missing.
func postgresPrimaryMissingSize(parent, size types.String) bool {
	if !parent.IsNull() && !parent.IsUnknown() && strings.TrimSpace(parent.ValueString()) != "" {
		return false
	}
	if parent.IsUnknown() || size.IsUnknown() {
		return false
	}
	return size.IsNull() || size.ValueString() == ""
}

// postgresReadReplicaBody builds the read-replica create body. The endpoint
// (POST .../postgres/{parent}/read-replica) accepts only {name, pg_config,
// pgbouncer_config, tags}; the parent-inherited inputs are rejected at plan time via
// ConflictsWith(parent). Unset (null) and still-computed (unknown) optionals are
// omitted so the request never serializes an empty or placeholder value.
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

// postgresConfigToBody converts a pg_config/pgbouncer_config map attribute into the
// request shape, omitting it when unset or still computed.
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

// postgresTagsToBody converts the tags list attribute into the request shape,
// omitting it when unset or still computed.
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

// createPostgresReadReplica dispatches a parent-set create to the read-replica child
// endpoint. The parent attribute is the path reference (name or id) of an existing
// parent in the same project/location; the response is the standard PostgresDatabase
// shape, so the caller maps it with setPostgresStateResource.
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
