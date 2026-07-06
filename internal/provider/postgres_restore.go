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

// Any KNOWN configured value, blank included, is a restore attempt: a malformed restore must
// error on the restore path, never fall through to the replica branch. The RFC 3339 validator
// rejects it at plan; time.Parse in the body builder backstops.
func postgresHasRestoreTarget(state *resource_postgres.PostgresModel) bool {
	return !state.RestoreTarget.IsNull() && !state.RestoreTarget.IsUnknown()
}

// The endpoint accepts {name, restore_target, pg_config, pgbouncer_config, tags}; unset or
// unknown optionals are omitted. restore_target parses into the time.Time the API expects.
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

// parent is the source to restore FROM; the response reports it with read_replica false.
func (r *postgresResource) createPostgresRestore(ctx context.Context, state *resource_postgres.PostgresModel) (*ubicloud_client.PostgresDatabase, diag.Diagnostics) {
	body, diags := postgresRestoreBody(ctx, state)
	if diags.HasError() {
		return nil, diags
	}

	// An interpolated source that resolved empty must not post to .../postgres//restore.
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
