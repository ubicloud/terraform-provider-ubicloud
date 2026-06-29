package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
)

// postgresHasRestoreTarget reports whether the plan asks for a point-in-time restore: a
// known, configured restore_target. A normal create and a read replica both leave it null.
// Any known value (including a blank one) is a restore attempt, not "unset": restore_target
// is a timestamp, so a configured-but-blank value is a malformed restore that must error on
// the restore path, never silently fall through to the read-replica branch. A blank/malformed
// value is rejected at plan by the RFC 3339 validator; createPostgresRestore (time.Parse)
// is the backstop. The source database is given as parent (AlsoRequires(parent)).
func postgresHasRestoreTarget(state *resource_postgres.PostgresModel) bool {
	return !state.RestoreTarget.IsNull() && !state.RestoreTarget.IsUnknown()
}

// postgresRestoreBody builds the restore create body. The endpoint
// (POST .../postgres/{parent}/restore) accepts {name, restore_target, pg_config,
// pgbouncer_config, tags}; the source-inherited inputs are rejected at plan time via
// ConflictsWith(parent). restore_target is the required point in time, parsed from the
// user's RFC 3339 string into the time.Time the API expects. Unset (null) and still-computed
// (unknown) optionals are omitted so the request never serializes an empty or placeholder
// value.
func postgresRestoreBody(ctx context.Context, state *resource_postgres.PostgresModel) (ubicloud_client.RestorePostgresDatabaseJSONRequestBody, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := ubicloud_client.RestorePostgresDatabaseJSONRequestBody{
		Name: state.Name.ValueString(),
	}

	restoreTarget, err := time.Parse(time.RFC3339, strings.TrimSpace(state.RestoreTarget.ValueString()))
	if err != nil {
		diags.AddAttributeError(
			path.Root("restore_target"),
			"Invalid restore_target",
			fmt.Sprintf("restore_target must be an RFC 3339 timestamp (e.g. 2026-06-24T11:00:00Z): %s", err.Error()),
		)
		return body, diags
	}
	body.RestoreTarget = restoreTarget

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

// createPostgresRestore dispatches a restore-target-set create to the restore child
// endpoint. parent is the path reference (name or id) of the source database to restore
// FROM (required alongside restore_target); the response is the standard PostgresDatabase
// shape (parent set to the source, read_replica false), so the caller maps it with
// setPostgresStateResource.
func (r *postgresResource) createPostgresRestore(ctx context.Context, state *resource_postgres.PostgresModel) (*ubicloud_client.PostgresDatabase, diag.Diagnostics) {
	body, diags := postgresRestoreBody(ctx, state)
	if diags.HasError() {
		return nil, diags
	}

	// parent is the source to restore FROM; AlsoRequires(parent) and the ModifyPlan blank
	// check reject a missing/blank source at plan, but guard here too so an interpolated
	// parent that resolves empty errors cleanly instead of posting to .../postgres//restore.
	source := strings.TrimSpace(state.Parent.ValueString())
	if source == "" {
		diags.AddError(
			"Missing restore source",
			fmt.Sprintf("parent must name the source database to restore from: %s", postgresResourceLogIdentifier(state)),
		)
		return nil, diags
	}
	postgresResp, err := r.uc.client.RestorePostgresDatabaseWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), source, body)
	if err != nil {
		diags.AddError(
			fmt.Sprintf("Error restoring postgres database: %s (source %s)", postgresResourceLogIdentifier(state), source),
			err.Error(),
		)
		return nil, diags
	}

	if postgresResp.StatusCode() != http.StatusOK {
		diags.AddError(
			"Unexpected HTTP status code restoring postgres database",
			fmt.Sprintf("Received %s for postgres restore: %s (source %s). Details: %s", postgresResp.Status(), postgresResourceLogIdentifier(state), source, postgresResp.Body))
		return nil, diags
	}

	return postgresResp.JSON200, diags
}
