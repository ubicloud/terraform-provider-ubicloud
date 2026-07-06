package provider

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

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

// The backend advances exactly one major, server-chosen (POST .../upgrade), so only a
// one-major increase is expressible; anything else returns a non-empty (summary, detail).
func postgresVersionUpgradeError(planVer, stateVer string) (summary, detail string) {
	planV, err1 := strconv.Atoi(planVer)
	stateV, err2 := strconv.Atoi(stateVer)
	if err1 != nil || err2 != nil {
		return "Invalid postgres version", fmt.Sprintf("Could not interpret a postgres version change from %q to %q.", stateVer, planVer)
	}
	if planV == stateV+1 {
		return "", ""
	}
	return "Unsupported postgres version change",
		fmt.Sprintf("A postgres major version advances exactly one major at a time, server-chosen (e.g. %d to %d). A change from %d to %d is not supported; upgrade one major at a time.", stateV, stateV+1, stateV, planV)
}

// Any mutable change OTHER than version; the irreversible upgrade never combines with one.
func postgresNonVersionMutableChanged(plan, state *resource_postgres.PostgresModel) bool {
	return postgresAttrChanged(plan.Size, state.Size) ||
		postgresAttrChanged(plan.StorageSize, state.StorageSize) ||
		postgresAttrChanged(plan.HaType, state.HaType) ||
		postgresAttrChanged(plan.Tags, state.Tags) ||
		postgresAttrChanged(plan.PgConfig, state.PgConfig) ||
		postgresAttrChanged(plan.PgbouncerConfig, state.PgbouncerConfig) ||
		postgresMaintenanceWindowChanged(plan, state) ||
		postgresAttrChanged(plan.Name, state.Name)
}

// A null window is unmanaged and never dispatches: terraform has no declarative unset (the
// CLI unset stays imperative). USFU pins an omitted config to prior. Hour 0 is a real value.
func postgresMaintenanceWindowChanged(plan, state *resource_postgres.PostgresModel) bool {
	return !plan.MaintenanceWindowStartAt.IsUnknown() &&
		!plan.MaintenanceWindowStartAt.IsNull() &&
		!plan.MaintenanceWindowStartAt.Equal(state.MaintenanceWindowStartAt)
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

// target_version already equals the plan while version lags: the server is already upgrading.
func postgresUpgradeInFlight(plan, state *resource_postgres.PostgresModel) bool {
	if state.TargetVersion.IsNull() || state.TargetVersion.IsUnknown() {
		return false
	}
	return state.TargetVersion.ValueString() == plan.Version.ValueString() &&
		state.TargetVersion.ValueString() != state.Version.ValueString()
}

// FAILS CLOSED: proceed only on a positively read non-failed status; an unreadable status
// raises rather than re-masking a failed upgrade. The endpoint's sole 400 is "Database is
// not upgrading", which means the upgrade already converged (success).
func (r *postgresResource) checkPostgresUpgradeFailed(ctx context.Context, projectId, location, name, logID string) diag.Diagnostics {
	var diags diag.Diagnostics
	statusResp, err := r.uc.client.GetPostgresDatabaseUpgradeStatusWithResponse(ctx, projectId, location, name)
	if err != nil {
		diags.AddError(fmt.Sprintf("Error reading postgres upgrade status: %s", logID), err.Error())
		return diags
	}
	// "Database is not upgrading": the upgrade already converged (success); proceed.
	if statusResp.StatusCode() == http.StatusBadRequest {
		return diags
	}
	if statusResp.StatusCode() != http.StatusOK || statusResp.JSON200 == nil {
		diags.AddError(
			"Unexpected HTTP status reading postgres upgrade status",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", statusResp.Status(), logID, statusResp.Body))
		return diags
	}
	if statusResp.JSON200.UpgradeStatus == "failed" {
		stage := ""
		if s := statusResp.JSON200.UpgradeStage; s != nil {
			stage = *s
		}
		diags.AddError(
			"Postgres major version upgrade failed",
			fmt.Sprintf("The in-flight upgrade to version %s has failed (stage %q): %s. Resolve it out of band (see `ubi pg ... show-upgrade-status`) before retrying.", string(statusResp.JSON200.TargetVersion), stage, logID))
	}
	return diags
}

// The target major is server-chosen, so the request carries no body.
func (r *postgresResource) applyPostgresUpgrade(ctx context.Context, projectId, location, name, logID string) diag.Diagnostics {
	upgradeResp, err := r.uc.client.UpgradePostgresDatabaseWithResponse(ctx, projectId, location, name)
	return postgresApplyDiags(err, upgradeResp, func() []byte { return upgradeResp.Body }, "upgrading postgres database", logID)
}

// The PATCH endpoint defaults every omitted field to its current value, so only changed
// fields are sent; ok=false means nothing in the PATCH set changed.
func postgresPatchBody(ctx context.Context, plan, state *resource_postgres.PostgresModel) (ubicloud_client.PatchPostgresDatabaseJSONRequestBody, bool, diag.Diagnostics) {
	var body ubicloud_client.PatchPostgresDatabaseJSONRequestBody
	var diags diag.Diagnostics
	ok := false

	if postgresAttrChanged(plan.Size, state.Size) {
		body.Size = plan.Size.ValueStringPointer()
		ok = true
	}
	if postgresAttrChanged(plan.StorageSize, state.StorageSize) {
		storageSize := int(plan.StorageSize.ValueInt64())
		body.StorageSize = &storageSize
		ok = true
	}
	if postgresAttrChanged(plan.HaType, state.HaType) {
		body.HaType = plan.HaType.ValueStringPointer()
		ok = true
	}
	if postgresAttrChanged(plan.Tags, state.Tags) {
		tags, d := postgresTagsToBody(ctx, plan.Tags)
		diags.Append(d...)
		body.Tags = tags
		ok = true
	}

	return body, ok, diags
}

// The server PATCH does existing.merge(supplied).compact: present keys overwrite, nulls
// delete. Tombstones come from the SERVER's keys (union prior state as the nil-serverMap
// fallback), so a key that drifted in out of band or that stale state missed is deleted.
func postgresConfigReplaceMap(ctx context.Context, plan, state types.Map, serverMap map[string]string) (map[string]*string, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := map[string]*string{}

	planPlain := map[string]string{}
	if !plan.IsNull() && !plan.IsUnknown() {
		diags.Append(plan.ElementsAs(ctx, &planPlain, false)...)
	}
	for k, v := range planPlain {
		value := v
		out[k] = &value
	}

	statePlain := map[string]string{}
	if !state.IsNull() && !state.IsUnknown() {
		diags.Append(state.ElementsAs(ctx, &statePlain, false)...)
	}
	tombstone := func(keys map[string]string) {
		for k := range keys {
			if _, ok := planPlain[k]; !ok {
				out[k] = nil
			}
		}
	}
	tombstone(serverMap)
	tombstone(statePlain)
	return out, diags
}

func postgresConfigChanged(plan, state *resource_postgres.PostgresModel) bool {
	return postgresAttrChanged(plan.PgConfig, state.PgConfig) ||
		postgresAttrChanged(plan.PgbouncerConfig, state.PgbouncerConfig)
}

// Sends ONLY the changed map: the endpoint merges each supplied map and leaves an omitted
// one untouched, so the companion can never be wiped. The POST full-replace endpoint would
// require both maps and wipe live config on an imported or stale resource. ok=false: no change.
func postgresConfigPatchBody(ctx context.Context, plan, state *resource_postgres.PostgresModel, serverCfg *ubicloud_client.PostgresConfig) (ubicloud_client.PatchPostgresDatabaseConfigJSONRequestBody, diag.Diagnostics) {
	var body ubicloud_client.PatchPostgresDatabaseConfigJSONRequestBody
	var diags diag.Diagnostics

	pgChanged := postgresAttrChanged(plan.PgConfig, state.PgConfig)
	pgbChanged := postgresAttrChanged(plan.PgbouncerConfig, state.PgbouncerConfig)

	var serverPg, serverPgb map[string]string
	if serverCfg != nil {
		serverPg = serverCfg.PgConfig
		serverPgb = serverCfg.PgbouncerConfig
	}

	if pgChanged {
		pgConfig, d := postgresConfigReplaceMap(ctx, plan.PgConfig, state.PgConfig, serverPg)
		diags.Append(d...)
		variant := ubicloud_client.PostgresPatchConfig0{PgConfig: pgConfig}
		if pgbChanged {
			pgbouncerConfig, d := postgresConfigReplaceMap(ctx, plan.PgbouncerConfig, state.PgbouncerConfig, serverPgb)
			diags.Append(d...)
			variant.PgbouncerConfig = &pgbouncerConfig
		}
		if err := body.FromPostgresPatchConfig0(variant); err != nil {
			diags.AddError("Error building postgres config patch body", err.Error())
		}
		return body, diags
	}

	pgbouncerConfig, d := postgresConfigReplaceMap(ctx, plan.PgbouncerConfig, state.PgbouncerConfig, serverPgb)
	diags.Append(d...)
	if err := body.FromPostgresPatchConfig1(ubicloud_client.PostgresPatchConfig1{PgbouncerConfig: pgbouncerConfig}); err != nil {
		diags.AddError("Error building postgres config patch body", err.Error())
	}
	return body, diags
}

// Renders a mutation call's outcome: the transport error, or the non-200 status with the
// response body. body is a closure so a nil response is never dereferenced on the error arm.
func postgresApplyDiags(err error, resp interface {
	StatusCode() int
	Status() string
}, body func() []byte, action, logID string) diag.Diagnostics {
	var diags diag.Diagnostics
	if err != nil {
		diags.AddError(fmt.Sprintf("Error %s: %s", action, logID), err.Error())
		return diags
	}
	if resp.StatusCode() != http.StatusOK {
		diags.AddError(
			"Unexpected HTTP status code "+action,
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", resp.Status(), logID, body()))
	}
	return diags
}

func (r *postgresResource) applyPostgresPatch(ctx context.Context, projectId, location, name string, body ubicloud_client.PatchPostgresDatabaseJSONRequestBody, logID string) diag.Diagnostics {
	postgresResp, err := r.uc.client.PatchPostgresDatabaseWithResponse(ctx, projectId, location, name, body)
	return postgresApplyDiags(err, postgresResp, func() []byte { return postgresResp.Body }, "updating postgres database", logID)
}

// Returns only diagnostics: callers hold the config maps at plan values, and the post-merge
// server config is deliberately NOT read back (it would resurface out-of-band drift).
func (r *postgresResource) applyPostgresConfigMerge(ctx context.Context, projectId, location, name string, body ubicloud_client.PatchPostgresDatabaseConfigJSONRequestBody, logID string) diag.Diagnostics {
	configResp, err := r.uc.client.PatchPostgresDatabaseConfigWithResponse(ctx, projectId, location, name, body)
	return postgresApplyDiags(err, configResp, func() []byte { return configResp.Body }, "updating postgres database config", logID)
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

// The path carries the OLD name; callers run this LAST so mutations address the old name.
func (r *postgresResource) applyPostgresRename(ctx context.Context, projectId, location, oldName, newName, logID string) diag.Diagnostics {
	renameResp, err := r.uc.client.RenamePostgresWithResponse(ctx, projectId, location, oldName, ubicloud_client.RenamePostgresJSONRequestBody{Name: newName})
	return postgresApplyDiags(err, renameResp, func() []byte { return renameResp.Body }, "renaming postgres database", logID)
}

// The window has its own endpoint with no state guard; the server stores the hour verbatim,
// so callers hold the request in state and only the status is checked here.
func (r *postgresResource) applyPostgresSetMaintenanceWindow(ctx context.Context, projectId, location, name string, startHour int64, logID string) diag.Diagnostics {
	hour := int(startHour)
	body := ubicloud_client.SetMaintenanceWindowJSONRequestBody{MaintenanceWindowStartAt: &hour}
	setResp, err := r.uc.client.SetMaintenanceWindowWithResponse(ctx, projectId, location, name, body)
	return postgresApplyDiags(err, setResp, func() []byte { return setResp.Body }, "setting postgres maintenance window", logID)
}
