package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const postgresStateRunning = "running"

// Vars, not consts, so tests can shorten them; production never reassigns them.
var (
	postgresCreateTimeoutDefault = 60 * time.Minute
	postgresDeleteTimeoutDefault = 60 * time.Minute
	postgresPollInterval         = 10 * time.Second
	postgresAdoptLookupBudget    = 30 * time.Second
	postgresAdoptClockSkew       = 30 * time.Second
	postgresWaitTransientBudget  = 6
)

var (
	_ resource.Resource                = &postgresResource{}
	_ resource.ResourceWithConfigure   = &postgresResource{}
	_ resource.ResourceWithImportState = &postgresResource{}
	_ resource.ResourceWithModifyPlan  = &postgresResource{}
)

// ModifyPlan enforces the plan-time invariants. It reads CONFIG for user intent: an omitted
// computed is unknown in the plan but a known null in config; an unknown value defers to apply.
func (r *postgresResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		var config resource_postgres.PostgresModel
		resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
		if resp.Diagnostics.HasError() {
			return
		}
		// Mutually exclusive shapes; a blank parent draws one clear error rather than silently
		// creating a primary (postgresHasParent trims a blank parent to unset).
		parentBlank := postgresParentBlank(config.Parent)
		switch {
		case parentBlank:
			resp.Diagnostics.AddAttributeError(
				path.Root("parent"),
				"Blank parent",
				"parent cannot be blank; omit it to create a primary database, or set it to the source database to restore from or replicate.",
			)
		case postgresPrimaryMissingSize(config.Parent, config.Size):
			resp.Diagnostics.AddAttributeError(
				path.Root("size"),
				"Missing size for primary postgres database",
				"size is required to create a primary postgres database; omit it only for a read replica (set parent) or a restore (set parent and restore_target).",
			)
		}
		return
	}
}

func NewPostgresResource() resource.Resource {
	return &postgresResource{}
}

type postgresResource struct {
	uc *UbicloudClient
}

func (r *postgresResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	uc, ok := req.ProviderData.(UbicloudClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *UbicloudClient, got: %T. Please report this issue to support@ubicloud.com.", req.ProviderData),
		)
		return
	}

	r.uc = &uc
}

func (r *postgresResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_postgres"
}

func (r *postgresResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_postgres.PostgresResourceSchema(ctx)
	resp.Schema.Description = "Provides a Ubicloud Postgres resource. This can be used to create and delete PostgreSQL databases."
	// The jq-injected block exists only so the generated model gets its Timeouts field; the
	// helper block carries the duration validators and descriptions.
	resp.Schema.Blocks["timeouts"] = timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true})
}

func (r *postgresResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_postgres.PostgresModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Capture whether tags is UNMANAGED before responses overwrite the plan value: a config-null
	// resource must persist tags=null or the next plan phantoms (see the ModifyPlan re-pin).
	tagsUnmanaged := state.Tags.IsNull() || state.Tags.IsUnknown()

	// Backstops an interpolation that resolved to whitespace.
	if postgresParentBlank(state.Parent) {
		resp.Diagnostics.AddAttributeError(
			path.Root("parent"),
			"Blank parent",
			"parent cannot be blank; omit it to create a primary database, or set it to the source database to replicate.",
		)
		return
	}

	// opCtx bounds every network call by timeouts.create; state writes keep ctx so partial
	// state still persists after a timeout.
	createTimeout, timeoutDiags := state.Timeouts.Create(ctx, postgresCreateTimeoutDefault)
	resp.Diagnostics.Append(timeoutDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	// Stamp before the create POST so an ambiguous-failure adopt has a non-zero lower bound.
	dispatchAt := time.Now()
	tflog.Debug(ctx, fmt.Sprintf("Creating postgres database: %s", postgresResourceLogIdentifier(&state)))
	postgresd, diags := r.createPostgresPrimary(opCtx, &state)
	if diags.HasError() {
		if opd := postgresOpContextDiags(opCtx, ctx, "creating", "create", postgresResourceLogIdentifier(&state), createTimeout); opd != nil {
			// The POST may have committed server-side; adopt so the row is tracked, not orphaned.
			r.adoptCreatedPostgres(ctx, dispatchAt, &state, resp)
			resp.Diagnostics.Append(opd...)
			return
		}
		// A definitive HTTP error is not adopted: the backend answers a name conflict with a
		// generic 500, indistinguishable from a post-commit 5xx, so adopting could claim a foreign row.
		resp.Diagnostics.Append(diags...)
		return
	}
	resp.Diagnostics.Append(diags...)

	// A 200 with a non-JSON body (an interposing proxy) is ambiguous like a timeout and can never
	// be the name-conflict 500, so adopt before failing closed.
	if postgresd == nil {
		r.adoptCreatedPostgres(ctx, dispatchAt, &state, resp)
		resp.Diagnostics.AddError(
			"Empty response creating postgres database",
			fmt.Sprintf("the API returned no database body: %s", postgresResourceLogIdentifier(&state)),
		)
		return
	}

	resp.Diagnostics.Append(setPostgresStateResource(ctx, postgresd, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The POST only ACCEPTS the create; block until running so apply completes usable.
	runningPg, waitDiags := r.waitForPostgresRunning(opCtx, ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), createTimeout, postgresResourceLogIdentifier(&state))
	if runningPg != nil {
		resp.Diagnostics.Append(setPostgresStateResource(ctx, runningPg, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if waitDiags.HasError() {
		// Persist the partial state so the timed-out create is tracked and replaced, then taint.
		ensurePostgresConfigKnown(&state)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		resp.Diagnostics.Append(waitDiags...)
		return
	}

	// The config GET can 500 at creating-state, so default still-unknown maps to empty after.
	resp.Diagnostics.Append(r.hydratePostgresConfig(opCtx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ensurePostgresConfigKnown(&state)

	// The best-effort hydration swallows a deadline; surface it as the bounded-create timeout.
	if opd := postgresOpContextDiags(opCtx, ctx, "creating", "create", postgresResourceLogIdentifier(&state), createTimeout); opd != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		resp.Diagnostics.Append(opd...)
		return
	}

	// Both remaining exits persist state; normalize unmanaged tags once.
	setPostgresTagsNullIfUnmanaged(ctx, tagsUnmanaged, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// adoptCreatedPostgres recovers an AMBIGUOUS create (a bounded-out timeout/cancel, or a non-JSON
// 200): the server may have committed, so a bounded lookup adopts the row, but only one whose
// created_at is at or after dispatchAt less clock skew; an older row is a foreign database.
func (r *postgresResource) adoptCreatedPostgres(parentCtx context.Context, dispatchAt time.Time, state *resource_postgres.PostgresModel, resp *resource.CreateResponse) {
	if parentCtx.Err() != nil {
		return
	}
	budgetCtx, cancel := context.WithTimeout(parentCtx, postgresAdoptLookupBudget)
	defer cancel()
	for {
		postgresResp, err := r.uc.client.GetPostgresDatabaseDetailsWithResponse(budgetCtx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
		if err == nil && postgresResp.StatusCode() == http.StatusOK && postgresResp.JSON200 != nil {
			// Under the unique name constraint our own row can never appear behind a foreign one.
			if postgresResp.JSON200.CreatedAt.Before(dispatchAt.Add(-postgresAdoptClockSkew)) {
				return
			}
			if mapDiags := setPostgresStateResource(parentCtx, postgresResp.JSON200, state); mapDiags.HasError() {
				resp.Diagnostics.Append(mapDiags...)
				return
			}
			ensurePostgresConfigKnown(state)
			resp.Diagnostics.Append(resp.State.Set(parentCtx, state)...)
			return
		}
		select {
		case <-budgetCtx.Done():
			return
		case <-time.After(postgresPollInterval):
		}
	}
}

func (r *postgresResource) createPostgresPrimary(ctx context.Context, state *resource_postgres.PostgresModel) (*ubicloud_client.PostgresDatabase, diag.Diagnostics) {
	var diags diag.Diagnostics
	// size is computed_optional (a replica omits it); catch an interpolation that resolved blank.
	if state.Size.IsNull() || state.Size.IsUnknown() || strings.TrimSpace(state.Size.ValueString()) == "" {
		diags.AddError(
			"Missing size for primary postgres database",
			fmt.Sprintf("size is required to create a primary postgres database: %s", postgresResourceLogIdentifier(state)),
		)
		return nil, diags
	}
	// An omitted storage_size marshals as the non-omitempty 0, which the backend rejects opaquely.
	if state.StorageSize.IsNull() || state.StorageSize.IsUnknown() || state.StorageSize.ValueInt64() <= 0 {
		diags.AddError(
			"Missing storage_size for primary postgres database",
			fmt.Sprintf("storage_size is required to create a primary postgres database: %s", postgresResourceLogIdentifier(state)),
		)
		return nil, diags
	}
	body := ubicloud_client.CreatePostgresDatabaseJSONRequestBody{
		Size:        state.Size.ValueString(),
		StorageSize: int(state.StorageSize.ValueInt64()),
	}
	if state.HaType.ValueString() != "" {
		body.HaType = state.HaType.ValueStringPointer()
	}
	if state.Version.ValueString() != "" {
		version := ubicloud_client.CreatePostgresDatabaseJSONBodyVersion(state.Version.ValueString())
		body.Version = &version
	}
	if state.Flavor.ValueString() != "" {
		body.Flavor = state.Flavor.ValueStringPointer()
	}
	if !state.RestrictByDefault.IsNull() && !state.RestrictByDefault.IsUnknown() {
		body.RestrictByDefault = state.RestrictByDefault.ValueBoolPointer()
	}
	if state.PrivateSubnetName.ValueString() != "" {
		body.PrivateSubnetName = state.PrivateSubnetName.ValueStringPointer()
	}
	// Without the config maps the backend keeps defaults and the Read-back drifts state.
	pgConfig, d := postgresConfigToBody(ctx, state.PgConfig)
	diags.Append(d...)
	body.PgConfig = pgConfig
	pgbouncerConfig, d := postgresConfigToBody(ctx, state.PgbouncerConfig)
	diags.Append(d...)
	body.PgbouncerConfig = pgbouncerConfig
	tags, d := postgresTagsToBody(ctx, state.Tags)
	diags.Append(d...)
	body.Tags = tags
	if diags.HasError() {
		return nil, diags
	}

	postgresResp, err := r.uc.client.CreatePostgresDatabaseWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), body)
	if err != nil {
		diags.AddError(
			fmt.Sprintf("Error creating postgres database: %s", postgresResourceLogIdentifier(state)),
			err.Error(),
		)
		return nil, diags
	}

	if postgresResp.StatusCode() != http.StatusOK {
		diags.AddError(
			"Unexpected HTTP status code creating postgres database",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), postgresResourceLogIdentifier(state), postgresResp.Body))
		return nil, diags
	}

	return postgresResp.JSON200, diags
}

func (r *postgresResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_postgres.PostgresModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read has no config: a managed refresh keeps a null prior null; an import (id unset, since
	// ImportState writes only the triple) and an explicit-tags resource hydrate below.
	importing := state.Id.IsNull()
	tagsUnmanaged := state.Tags.IsNull()

	projectID := state.ProjectId.ValueString()
	location := state.Location.ValueString()
	name := state.Name.ValueString()

	tflog.Debug(ctx, fmt.Sprintf("Reading postgres database: %s", postgresResourceLogIdentifier(&state)))
	postgresResp, err := r.uc.client.GetPostgresDatabaseDetailsWithResponse(ctx, projectID, location, name)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading postgres database: %s", postgresResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if postgresResp.StatusCode() == http.StatusNotFound {
		// Deleted out of band: drop from state so the next plan converges instead of wedging.
		tflog.Debug(ctx, fmt.Sprintf("Postgres database not found, removing from state: %s", postgresResourceLogIdentifier(&state)))
		resp.State.RemoveResource(ctx)
		return
	}

	if postgresResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading postgres database",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), postgresResourceLogIdentifier(&state), postgresResp.Body))
		return
	}

	// A 200 with no JSON body is a proxy artifact, not a 404: fail closed, keep state.
	if postgresResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Empty response reading postgres database",
			fmt.Sprintf("the API returned no database body: %s", postgresResourceLogIdentifier(&state)),
		)
		return
	}

	diags := setPostgresStateResource(ctx, postgresResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.hydratePostgresConfig(ctx, projectID, location, name, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	setPostgresTagsNullIfUnmanaged(ctx, tagsUnmanaged && !importing, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// readPostgresConfig reads the user config maps the config PATCH merges against; best-effort:
// the read can fail at creating-state, so a transport error or non-200 returns nil.
func (r *postgresResource) readPostgresConfig(ctx context.Context, projectID, location, name string) *ubicloud_client.PostgresConfig {
	configResp, err := r.uc.client.GetPostgresDatabaseConfigWithResponse(ctx, projectID, location, name)
	if err != nil {
		tflog.Debug(ctx, fmt.Sprintf("Skipping postgres config read (transport error): %s: %s", name, err.Error()))
		return nil
	}
	if configResp.StatusCode() != http.StatusOK || configResp.JSON200 == nil {
		tflog.Debug(ctx, fmt.Sprintf("Skipping postgres config read (status %d): %s", configResp.StatusCode(), name))
		return nil
	}
	return configResp.JSON200
}

// Overlays the config read onto state (the detailed GET lacks the maps); nil leaves prior.
func (r *postgresResource) hydratePostgresConfig(ctx context.Context, projectID, location, name string, state *resource_postgres.PostgresModel) diag.Diagnostics {
	return applyPostgresConfigToState(ctx, r.readPostgresConfig(ctx, projectID, location, name), state)
}

// Defaults a still-unknown config map to empty so state is never unknown after apply.
func ensurePostgresConfigKnown(state *resource_postgres.PostgresModel) {
	empty := types.MapValueMust(types.StringType, map[string]attr.Value{})
	if state.PgConfig.IsUnknown() {
		state.PgConfig = empty
	}
	if state.PgbouncerConfig.IsUnknown() {
		state.PgbouncerConfig = empty
	}
}

func (r *postgresResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state resource_postgres.PostgresModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddError(
		"Update of postgres is not supported",
		fmt.Sprintf("Cannot update postgres database: %s", postgresResourceLogIdentifier(&state)))
}

func (r *postgresResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resource_postgres.PostgresModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	deleteTimeout, timeoutDiags := state.Timeouts.Delete(ctx, postgresDeleteTimeoutDefault)
	resp.Diagnostics.Append(timeoutDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()

	tflog.Debug(ctx, fmt.Sprintf("Deleting postgres database: %s", postgresResourceLogIdentifier(&state)))
	postgresResp, err := r.uc.client.DeletePostgresDatabaseWithResponse(opCtx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		if opd := postgresOpContextDiags(opCtx, ctx, "deleting", "delete", postgresResourceLogIdentifier(&state), deleteTimeout); opd != nil {
			resp.Diagnostics.Append(opd...)
			return
		}
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error deleting postgres database: %s", postgresResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if postgresResp.StatusCode() != http.StatusNoContent && postgresResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code deleting postgres database",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), postgresResourceLogIdentifier(&state), postgresResp.Body))
		return
	}

	// A missing postgres row itself deletes as 204; the 404 means the project or location is
	// gone (resolved before the postgres branch), taking the database with it, so converge.
	if postgresResp.StatusCode() == http.StatusNotFound {
		return
	}

	// The DELETE only ACCEPTS the teardown; block until the row is gone.
	resp.Diagnostics.Append(r.waitForPostgresDeleted(opCtx, ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), deleteTimeout, postgresResourceLogIdentifier(&state))...)
}

// Polls until running, bounded by opCtx; parentCtx only classifies (cancel vs timeout).
// display_state has no terminal failure state, so a stuck create surfaces as the timeout.
func (r *postgresResource) waitForPostgresRunning(opCtx, parentCtx context.Context, projectID, location, name string, timeout time.Duration, logID string) (*ubicloud_client.PostgresDatabase, diag.Diagnostics) {
	var diags diag.Diagnostics
	var lastPg *ubicloud_client.PostgresDatabase
	transientFailures := 0
	for {
		postgresResp, err := r.uc.client.GetPostgresDatabaseDetailsWithResponse(opCtx, projectID, location, name)
		switch {
		case err != nil:
			// A dead opCtx is the deadline, not a blip: classify before the budget masks it.
			if opCtx.Err() != nil {
				diags.Append(postgresRunningWaitContextDiags(parentCtx, logID, timeout, lastPg)...)
				return lastPg, diags
			}
			transientFailures++
			if transientFailures > postgresWaitTransientBudget {
				diags.AddError(
					fmt.Sprintf("Error polling postgres database while waiting for it to become ready: %s", logID),
					err.Error())
				return lastPg, diags
			}
		case postgresResp.StatusCode() == http.StatusOK:
			// Only a snapshot carrying a state clears the budget; a bodyless 200 or an error
			// envelope (empty-state struct) is an LB/proxy non-answer, so poll on without resetting.
			if postgresResp.JSON200 != nil && postgresResp.JSON200.State != "" {
				transientFailures = 0
				lastPg = postgresResp.JSON200
				if lastPg.State == postgresStateRunning {
					return lastPg, diags
				}
			}
		case postgresWaitStatusTransient(postgresResp.StatusCode()):
			transientFailures++
			if transientFailures > postgresWaitTransientBudget {
				diags.AddError(
					"Unexpected HTTP status code waiting for postgres database to become ready",
					fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), logID, postgresResp.Body))
				return lastPg, diags
			}
		default:
			diags.AddError(
				"Unexpected HTTP status code waiting for postgres database to become ready",
				fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), logID, postgresResp.Body))
			return lastPg, diags
		}
		select {
		case <-opCtx.Done():
			diags.Append(postgresRunningWaitContextDiags(parentCtx, logID, timeout, lastPg)...)
			return lastPg, diags
		case <-time.After(postgresPollInterval):
		}
	}
}

// 429 and 5xx are transient; any other non-200 is a terminal answer the budget must not mask.
func postgresWaitStatusTransient(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// A parent-context cause reports as cancellation, since raising timeouts.create cannot help.
func postgresRunningWaitContextDiags(parentCtx context.Context, logID string, timeout time.Duration, lastPg *ubicloud_client.PostgresDatabase) diag.Diagnostics {
	var diags diag.Diagnostics
	if parentCtx.Err() != nil {
		diags.AddError(
			"Cancelled while waiting for postgres database to become ready",
			fmt.Sprintf("%s: %s", logID, parentCtx.Err().Error()))
		return diags
	}
	lastState := "unknown"
	if lastPg != nil {
		lastState = lastPg.State
	}
	diags.AddError(
		"Timeout waiting for postgres database to become ready",
		fmt.Sprintf("postgres database %s did not reach state %q within %s (last observed state: %q). Increase timeouts.create to allow more time.", logID, postgresStateRunning, timeout, lastState))
	return diags
}

// waitForPostgresDeleted polls until the GET 404s, bounded by opCtx; parentCtx only classifies.
func (r *postgresResource) waitForPostgresDeleted(opCtx, parentCtx context.Context, projectID, location, name string, timeout time.Duration, logID string) diag.Diagnostics {
	var diags diag.Diagnostics
	transientFailures := 0
	for {
		postgresResp, err := r.uc.client.GetPostgresDatabaseDetailsWithResponse(opCtx, projectID, location, name)
		switch {
		case err != nil:
			// A dead opCtx is the deadline, not a blip: classify before the budget masks it.
			if opCtx.Err() != nil {
				diags.Append(postgresDeletedWaitContextDiags(parentCtx, logID, timeout)...)
				return diags
			}
			transientFailures++
			if transientFailures > postgresWaitTransientBudget {
				diags.AddError(
					fmt.Sprintf("Error polling postgres database while waiting for it to be deleted: %s", logID),
					err.Error())
				return diags
			}
		case postgresResp.StatusCode() == http.StatusNotFound:
			return diags
		case postgresResp.StatusCode() == http.StatusOK:
			// A snapshot carrying a state (row still present) clears the budget; a bodyless 200 or
			// error envelope is an LB/proxy non-answer, so poll on without resetting.
			if postgresResp.JSON200 != nil && postgresResp.JSON200.State != "" {
				transientFailures = 0
			}
		case postgresWaitStatusTransient(postgresResp.StatusCode()):
			transientFailures++
			if transientFailures > postgresWaitTransientBudget {
				diags.AddError(
					"Unexpected HTTP status code waiting for postgres database to be deleted",
					fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), logID, postgresResp.Body))
				return diags
			}
		default:
			diags.AddError(
				"Unexpected HTTP status code waiting for postgres database to be deleted",
				fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), logID, postgresResp.Body))
			return diags
		}
		select {
		case <-opCtx.Done():
			diags.Append(postgresDeletedWaitContextDiags(parentCtx, logID, timeout)...)
			return diags
		case <-time.After(postgresPollInterval):
		}
	}
}

// A parent-context cause reports as cancellation; otherwise the operation timeout fired.
func postgresDeletedWaitContextDiags(parentCtx context.Context, logID string, timeout time.Duration) diag.Diagnostics {
	var diags diag.Diagnostics
	if parentCtx.Err() != nil {
		diags.AddError(
			"Cancelled while waiting for postgres database to be deleted",
			fmt.Sprintf("%s: %s", logID, parentCtx.Err().Error()))
		return diags
	}
	diags.AddError(
		"Timeout waiting for postgres database to be deleted",
		fmt.Sprintf("postgres database %s was still present after %s. Increase timeouts.delete to allow more time.", logID, timeout))
	return diags
}

// postgresOpContextDiags classifies a terminated op context at a non-poll step: nil while
// live, cancellation when the parent ended (raising timeouts.* cannot help), else the timeout.
func postgresOpContextDiags(opCtx, parentCtx context.Context, verb, knob, logID string, timeout time.Duration) diag.Diagnostics {
	if opCtx.Err() == nil {
		return nil
	}
	var diags diag.Diagnostics
	if parentCtx.Err() != nil {
		diags.AddError(
			fmt.Sprintf("Cancelled while %s postgres database", verb),
			fmt.Sprintf("%s: %s", logID, parentCtx.Err().Error()))
		return diags
	}
	diags.AddError(
		fmt.Sprintf("Timeout while %s postgres database", verb),
		fmt.Sprintf("postgres database %s did not finish %s within %s. Increase timeouts.%s to allow more time.", logID, verb, timeout, knob))
	return diags
}

func (r *postgresResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	idParts := strings.Split(req.ID, ",")

	if len(idParts) != 3 || idParts[0] == "" || idParts[1] == "" || idParts[2] == "" {
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			fmt.Sprintf("Expected import identifier with format: project_id,location,name. Got: %q", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), idParts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("location"), idParts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), idParts[2])...)
}

func setPostgresStateResource(ctx context.Context, postgresd *ubicloud_client.PostgresDatabase, state *resource_postgres.PostgresModel) diag.Diagnostics {
	state.Id = types.StringValue(postgresd.Id)
	state.Name = types.StringValue(postgresd.Name)
	state.State = types.StringValue(postgresd.State)
	state.Location = types.StringValue(postgresd.Location)
	state.VmSize = types.StringValue(postgresd.VmSize)
	state.Size = types.StringValue(postgresd.VmSize)
	state.StorageSizeGib = types.Int64Value(int64(postgresd.StorageSizeGib))
	state.StorageSize = types.Int64Value(int64(postgresd.StorageSizeGib))
	state.Primary = types.BoolValue(postgresd.Primary)
	state.HaType = types.StringValue(postgresd.HaType)
	state.Version = types.StringValue(string(postgresd.Version))
	state.ConnectionString = types.StringPointerValue(postgresd.ConnectionString)
	state.EarliestRestoreTime = types.StringPointerValue(postgresd.EarliestRestoreTime)
	state.LatestRestoreTime = types.StringValue(postgresd.LatestRestoreTime)
	state.Flavor = types.StringValue(postgresd.Flavor)
	state.TargetVmSize = types.StringPointerValue(postgresd.TargetVmSize)
	state.TargetStorageSizeGib = int64PointerValue(postgresd.TargetStorageSizeGib)
	state.TargetVersion = types.StringValue(string(postgresd.TargetVersion))
	state.TargetServerCount = types.Int64Value(int64(postgresd.TargetServerCount))
	state.MaintenanceWindowStartAt = int64PointerValue(postgresd.MaintenanceWindowStartAt)
	state.ReadReplica = types.BoolValue(postgresd.ReadReplica)
	// The API reports parent as its canonical PATH, not the configured name/id; fill it only
	// when unset so a user-supplied reference round-trips (an import gets the path).
	if state.Parent.IsNull() || state.Parent.IsUnknown() {
		state.Parent = types.StringPointerValue(postgresd.Parent)
	}
	state.FallbackActive = types.BoolValue(postgresd.FallbackActive)
	state.CaCertificates = types.StringPointerValue(postgresd.CaCertificates)
	state.CreatedAt = types.StringValue(postgresd.CreatedAt.Format(iso8601Layout))
	state.Hostname = types.StringPointerValue(postgresd.Hostname)
	state.Username = types.StringPointerValue(postgresd.Username)
	state.Password = types.StringPointerValue(postgresd.Password)

	firewallRulesListValue, diags := GetPostgresFirewallRulesState(ctx, postgresd.FirewallRules)
	if diags.HasError() {
		return diags
	}
	state.FirewallRules = firewallRulesListValue

	tagsListValue, tagsDiags := GetPostgresTagsState(ctx, postgresd.Tags)
	diags.Append(tagsDiags...)
	if diags.HasError() {
		return diags
	}
	state.Tags = tagsListValue

	return diags
}

// The typed null tags list a config-null resource persists: the next proposed null equals prior.
func postgresTagsNull(ctx context.Context) types.List {
	return types.ListNull(resource_postgres.TagsValue{}.Type(ctx))
}

func setPostgresTagsNullIfUnmanaged(ctx context.Context, unmanaged bool, state *resource_postgres.PostgresModel) {
	if unmanaged {
		state.Tags = postgresTagsNull(ctx)
	}
}

func postgresResourceLogIdentifier(state *resource_postgres.PostgresModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
