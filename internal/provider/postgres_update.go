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

// postgresAttrChanged reports a user-driven change: a KNOWN plan value that differs from
// state. An unknown plan value is a computed the framework still has to resolve, not a
// change, so an unpinned computed never trips a mutation.
func postgresAttrChanged(planVal, stateVal attr.Value) bool {
	return !planVal.IsUnknown() && !planVal.Equal(stateVal)
}

// postgresVersionUpgradeError validates an intended major version change. The backend
// advances exactly one major at a time, server-chosen (POST .../upgrade sets target_version
// = version + 1), so only an increase of one major is expressible. A downgrade or a
// multi-major jump returns a non-empty (summary, detail); a valid one-major increase
// returns ("", ""). Callers gate on postgresAttrChanged first, so an equal pair never
// reaches here.
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

// postgresNonVersionMutableChanged reports whether any mutable attribute OTHER than version
// changed. A major version upgrade is an isolated imperative operation (the ubi CLI exposes
// it as `ubi pg upgrade`, not a `ubi pg modify` option) and is irreversible, so the provider
// refuses to combine it with PATCH/config/rename mutations: dispatching the upgrade first and
// then erroring on a later call would leave an irreversible upgrade scheduled with state not
// reconciled to the mutation that failed.
func postgresNonVersionMutableChanged(plan, state *resource_postgres.PostgresModel) bool {
	return postgresAttrChanged(plan.Size, state.Size) ||
		postgresAttrChanged(plan.StorageSize, state.StorageSize) ||
		postgresAttrChanged(plan.HaType, state.HaType) ||
		postgresAttrChanged(plan.Tags, state.Tags) ||
		postgresAttrChanged(plan.PgConfig, state.PgConfig) ||
		postgresAttrChanged(plan.PgbouncerConfig, state.PgbouncerConfig) ||
		postgresAttrChanged(plan.Name, state.Name)
}

// postgresUpgradeInFlight reports that the server is already advancing the major version to
// the planned target: target_version already equals the planned version while the actual
// version still lags. A fresh POST .../upgrade in this window is unnecessary and would fail
// the backend convergence precheck, so Update skips it and just holds the planned version.
func postgresUpgradeInFlight(plan, state *resource_postgres.PostgresModel) bool {
	if state.TargetVersion.IsNull() || state.TargetVersion.IsUnknown() {
		return false
	}
	return state.TargetVersion.ValueString() == plan.Version.ValueString() &&
		state.TargetVersion.ValueString() != state.Version.ValueString()
}

// checkPostgresUpgradeFailed queries GET .../upgrade on the in-flight skip path and FAILS
// CLOSED: it only lets Update proceed (and thus hold the planned version) when it positively
// confirms the upgrade is not failed. A 200 with upgrade_status == "failed" raises a clear
// error; a transport error or an unexpected non-200 also raises, so a status it cannot read
// is never silently treated as success (which would re-mask a failed upgrade). The sole
// proceed-without-200 case is a 400, which this endpoint returns only as "Database is not
// upgrading": that means the upgrade already converged (target_version == version
// server-side), i.e. success, so it is not an error.
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

// applyPostgresUpgrade issues the imperative one-major upgrade (POST .../upgrade). The
// target major is server-chosen, so the request carries no body.
func (r *postgresResource) applyPostgresUpgrade(ctx context.Context, projectId, location, name, logID string) diag.Diagnostics {
	var diags diag.Diagnostics
	upgradeResp, err := r.uc.client.UpgradePostgresDatabaseWithResponse(ctx, projectId, location, name)
	if err != nil {
		diags.AddError(fmt.Sprintf("Error upgrading postgres database: %s", logID), err.Error())
		return diags
	}
	if upgradeResp.StatusCode() != http.StatusOK {
		diags.AddError(
			"Unexpected HTTP status code upgrading postgres database",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", upgradeResp.Status(), logID, upgradeResp.Body))
	}
	return diags
}

// postgresPatchBody builds the PATCH body from the resize/HA/tags fields that changed.
// The endpoint defaults every omitted field to the current value, so only changed fields
// are sent; ok=false means nothing in the PATCH set changed. The body is
// additionalProperties:false {ha_type,size,storage_size,tags}, all optional.
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

// postgresConfigReplaceMap builds the PATCH wire map for a declarative replace of one
// config map: every desired (plan) key carries its value, and every key present in prior
// state but absent from the plan carries an explicit null. The server PATCH does
// existing.merge(supplied).compact, so the present keys overwrite and the nulls delete, and
// the resulting server map equals the plan exactly. A key the server holds that is in
// neither plan nor state is left untouched, which is a benign under-deletion (it resurfaces
// on the next refresh once state is hydrated) and never a companion wipe, since the
// companion map is omitted from the request entirely by postgresConfigPatchBody.
func postgresConfigReplaceMap(ctx context.Context, plan, state types.Map) (map[string]*string, diag.Diagnostics) {
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
	for k := range statePlain {
		if _, ok := planPlain[k]; !ok {
			out[k] = nil
		}
	}
	return out, diags
}

// postgresConfigPatchBody builds the config PATCH body (patchPostgresDatabaseConfig),
// including ONLY the map that changed and, within it, a full declarative replace: present
// keys with values plus dropped keys as explicit null (see postgresConfigReplaceMap). The
// endpoint merges each supplied map into the server's current config (then compacts), and
// leaves an OMITTED map untouched, so changing pg_config never disturbs pgbouncer_config
// (and vice versa). The POST full-replace endpoint is deliberately NOT used: it requires
// both maps, so it would send {} for the untouched companion and WIPE live server config on
// an imported or stale resource. Sending only the changed map makes a companion wipe
// structurally impossible regardless of how authoritative state is. ok=false means neither
// map changed.
func postgresConfigPatchBody(ctx context.Context, plan, state *resource_postgres.PostgresModel) (ubicloud_client.PatchPostgresDatabaseConfigJSONRequestBody, bool, diag.Diagnostics) {
	var body ubicloud_client.PatchPostgresDatabaseConfigJSONRequestBody
	var diags diag.Diagnostics

	pgChanged := postgresAttrChanged(plan.PgConfig, state.PgConfig)
	pgbChanged := postgresAttrChanged(plan.PgbouncerConfig, state.PgbouncerConfig)
	if !pgChanged && !pgbChanged {
		return body, false, diags
	}

	if pgChanged {
		pgConfig, d := postgresConfigReplaceMap(ctx, plan.PgConfig, state.PgConfig)
		diags.Append(d...)
		variant := ubicloud_client.PostgresPatchConfig0{PgConfig: pgConfig}
		if pgbChanged {
			pgbouncerConfig, d := postgresConfigReplaceMap(ctx, plan.PgbouncerConfig, state.PgbouncerConfig)
			diags.Append(d...)
			variant.PgbouncerConfig = &pgbouncerConfig
		}
		if err := body.FromPostgresPatchConfig0(variant); err != nil {
			diags.AddError("Error building postgres config patch body", err.Error())
		}
		return body, true, diags
	}

	pgbouncerConfig, d := postgresConfigReplaceMap(ctx, plan.PgbouncerConfig, state.PgbouncerConfig)
	diags.Append(d...)
	if err := body.FromPostgresPatchConfig1(ubicloud_client.PostgresPatchConfig1{PgbouncerConfig: pgbouncerConfig}); err != nil {
		diags.AddError("Error building postgres config patch body", err.Error())
	}
	return body, true, diags
}

// applyPostgresPatch issues the resize/HA/tags PATCH against the resource's current name.
func (r *postgresResource) applyPostgresPatch(ctx context.Context, projectId, location, name string, body ubicloud_client.PatchPostgresDatabaseJSONRequestBody, logID string) diag.Diagnostics {
	var diags diag.Diagnostics
	postgresResp, err := r.uc.client.PatchPostgresDatabaseWithResponse(ctx, projectId, location, name, body)
	if err != nil {
		diags.AddError(fmt.Sprintf("Error updating postgres database: %s", logID), err.Error())
		return diags
	}
	if postgresResp.StatusCode() != http.StatusOK {
		diags.AddError(
			"Unexpected HTTP status code updating postgres database",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), logID, postgresResp.Body))
	}
	return diags
}

// applyPostgresConfigMerge issues the config PATCH against the current name and returns the
// response, which carries the AUTHORITATIVE post-merge server config (pg.user_config /
// pg.pgbouncer_user_config). The caller overlays state from it so state equals the server
// exactly, even when prior state was stale or partial and the request therefore omitted a
// tombstone for a key the server still holds.
func (r *postgresResource) applyPostgresConfigMerge(ctx context.Context, projectId, location, name string, body ubicloud_client.PatchPostgresDatabaseConfigJSONRequestBody, logID string) (*ubicloud_client.PostgresConfig, diag.Diagnostics) {
	var diags diag.Diagnostics
	configResp, err := r.uc.client.PatchPostgresDatabaseConfigWithResponse(ctx, projectId, location, name, body)
	if err != nil {
		diags.AddError(fmt.Sprintf("Error updating postgres database config: %s", logID), err.Error())
		return nil, diags
	}
	if configResp.StatusCode() != http.StatusOK {
		diags.AddError(
			"Unexpected HTTP status code updating postgres database config",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", configResp.Status(), logID, configResp.Body))
		return nil, diags
	}
	return configResp.JSON200, diags
}

// applyPostgresConfigToState overlays the user config maps from an authoritative
// PostgresConfig (a GET .../config read-back, or the post-merge body the config PATCH
// returns) onto state, so state mirrors the server exactly. A nil cfg is a no-op.
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

// applyPostgresRename issues the rename POST. The path carries the OLD name (the resource's
// current server identity); newName is the body. Callers run this LAST so the content
// mutations above address the stable old name.
func (r *postgresResource) applyPostgresRename(ctx context.Context, projectId, location, oldName, newName, logID string) diag.Diagnostics {
	var diags diag.Diagnostics
	renameResp, err := r.uc.client.RenamePostgresWithResponse(ctx, projectId, location, oldName, ubicloud_client.RenamePostgresJSONRequestBody{Name: newName})
	if err != nil {
		diags.AddError(fmt.Sprintf("Error renaming postgres database: %s", logID), err.Error())
		return diags
	}
	if renameResp.StatusCode() != http.StatusOK {
		diags.AddError(
			"Unexpected HTTP status code renaming postgres database",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", renameResp.Status(), logID, renameResp.Body))
	}
	return diags
}
