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

// Wait-for-ready tuning. Create blocks until the database reports state=running so apply
// completes only when the resource is usable; Delete blocks until the row is gone. Both are
// bounded by the user-configurable timeouts block and default generously, since cloud-backed
// provisioning and async teardown each run over many respirate hops.
const postgresStateRunning = "running"

// These are vars, not consts, so tests can shorten them; production never reassigns them.
// postgresPollInterval is the wait-for-ready poll cadence; the timeout defaults apply when the
// timeouts block omits the corresponding duration. postgresAdoptLookupBudget bounds the recovery
// lookup that adopts an already-created database after a dispatch timeout (see
// adoptCreatedPostgresOnTimeout).
var (
	postgresCreateTimeoutDefault = 60 * time.Minute
	postgresDeleteTimeoutDefault = 60 * time.Minute
	postgresPollInterval         = 10 * time.Second
	postgresAdoptLookupBudget    = 30 * time.Second
)

var (
	_ resource.Resource                = &postgresResource{}
	_ resource.ResourceWithConfigure   = &postgresResource{}
	_ resource.ResourceWithImportState = &postgresResource{}
	_ resource.ResourceWithModifyPlan  = &postgresResource{}
)

// ModifyPlan enforces two plan-time invariants. On CREATE (no prior state) it checks the
// size/parent invariant: a primary needs size; a read replica omits it and inherits the
// parent's (size ConflictsWith parent). That branch reads the CONFIG, not the plan, since
// size is computed, so an omitted size is unknown in the plan but a known null in config,
// which distinguishes "the user left it out" from "it depends on an unresolved value"; a
// genuinely unknown (interpolated) size is deferred to createPostgresPrimary. On UPDATE it
// rejects an unsupported version change at plan time: a downgrade or multi-major jump (the
// upgrade endpoint advances exactly one major, server-chosen) and a version change combined
// with any other mutable change (an upgrade is irreversible and must be applied on its own),
// so these fail before apply rather than mid-orchestration.
func (r *postgresResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		var config resource_postgres.PostgresModel
		resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
		if resp.Diagnostics.HasError() {
			return
		}
		// A restore (restore_target set) inherits size from its source, like a read replica, so
		// the primary-size guard must skip it; otherwise an invalid restore_target with no parent
		// gets both the AlsoRequires(parent) error and a misleading "missing size" error.
		if !postgresHasRestoreTarget(&config) && postgresPrimaryMissingSize(config.Parent, config.Size) {
			resp.Diagnostics.AddAttributeError(
				path.Root("size"),
				"Missing size for primary postgres database",
				"size is required to create a primary postgres database; omit it only for a read replica (set parent) or a restore (set parent and restore_target).",
			)
		}
		// A restore must name a non-blank source via parent. AlsoRequires(parent) catches a
		// null parent; catch an explicitly blank one here before it becomes POST .../postgres//restore.
		if postgresHasRestoreTarget(&config) && !config.Parent.IsNull() && !config.Parent.IsUnknown() && strings.TrimSpace(config.Parent.ValueString()) == "" {
			resp.Diagnostics.AddAttributeError(
				path.Root("parent"),
				"Missing restore source",
				"parent must name the source database to restore from; it cannot be blank when restore_target is set.",
			)
		}
		return
	}

	// A null plan is a destroy: nothing to validate.
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan, state resource_postgres.PostgresModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if postgresAttrChanged(plan.Version, state.Version) {
		if summary, detail := postgresVersionUpgradeError(plan.Version.ValueString(), state.Version.ValueString()); summary != "" {
			resp.Diagnostics.AddAttributeError(path.Root("version"), summary, detail)
		} else if postgresNonVersionMutableChanged(&plan, &state) {
			resp.Diagnostics.AddAttributeError(
				path.Root("version"),
				"Postgres version upgrade must be applied on its own",
				"A major version upgrade cannot be combined with other changes (size, storage_size, ha_type, tags, pg_config, pgbouncer_config, name); apply the version change in a separate plan.")
		}
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
	// The generated schema carries a timeouts block, injected at the framework-IR layer
	// (config/inject_timeouts.jq) only so the generated model gets the matching Timeouts
	// field. Replace it here with the canonical helper block, which attaches the duration
	// validators and descriptions. Update is reserved for the convergence wait (resize and
	// version upgrade) tracked as a follow-up; Create and Delete are honored today.
	resp.Schema.Blocks["timeouts"] = timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true})
}

func (r *postgresResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_postgres.PostgresModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Bound the WHOLE create (the dispatch POST, the wait-for-running poll, and the config
	// hydration) by timeouts.create, so a stuck request at any step is cancelled at the deadline
	// instead of hanging on the provider-wide context. opCtx is used for every network call;
	// tfsdk state writes keep the original ctx so partial state still persists after a timeout.
	createTimeout, timeoutDiags := state.Timeouts.Create(ctx, postgresCreateTimeoutDefault)
	resp.Diagnostics.Append(timeoutDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	// A read replica and a point-in-time restore are each created off an existing source
	// through a separate child endpoint, not via the create body (which forbids parent and
	// restore_target). Dispatch on the inputs: restore_target set takes the restore path (its
	// source is parent, AlsoRequires(parent)); otherwise a known, non-empty parent takes the
	// read-replica path; a normal create leaves both unset. restore is checked first because a
	// restore also sets parent (the source), and the only response difference is read_replica.
	// Create reads the resolved plan, so a computed restore_target (e.g. the source's
	// latest_restore_time) is a KNOWN value here even when it was unknown at plan; the
	// known-only predicate never misclassifies it as a non-restore at apply.
	var postgresd *ubicloud_client.PostgresDatabase
	var diags diag.Diagnostics
	switch {
	case postgresHasRestoreTarget(&state):
		tflog.Debug(ctx, fmt.Sprintf("Restoring postgres database: %s (source %s, restore_target %s)", postgresResourceLogIdentifier(&state), state.Parent.ValueString(), state.RestoreTarget.ValueString()))
		postgresd, diags = r.createPostgresRestore(opCtx, &state)
	case postgresHasParent(&state):
		tflog.Debug(ctx, fmt.Sprintf("Creating postgres read replica: %s (parent %s)", postgresResourceLogIdentifier(&state), state.Parent.ValueString()))
		postgresd, diags = r.createPostgresReadReplica(opCtx, &state)
	default:
		tflog.Debug(ctx, fmt.Sprintf("Creating postgres database: %s", postgresResourceLogIdentifier(&state)))
		postgresd, diags = r.createPostgresPrimary(opCtx, &state)
	}
	if diags.HasError() {
		// A dispatch failure caused by the operation deadline or a parent cancellation is part of
		// the bounded-create behavior; report it as such rather than as a generic transport error.
		if opd := postgresOpContextDiags(opCtx, ctx, "creating", "create", postgresResourceLogIdentifier(&state), createTimeout); opd != nil {
			// The POST may have reached the server before the client gave up; adopt an
			// already-created database into state so a timed-out create is tracked (and replaced
			// on the next apply) instead of orphaned into a name conflict.
			r.adoptCreatedPostgresOnTimeout(ctx, &state, resp)
			resp.Diagnostics.Append(opd...)
			return
		}
		resp.Diagnostics.Append(diags...)
		return
	}
	resp.Diagnostics.Append(diags...)

	// Every dispatch path returns the PostgresDatabase body on HTTP 200; a nil here means the
	// API answered 200 with no JSON body (a contract violation committee blocks in TEST mode),
	// so fail closed rather than nil-deref in setPostgresStateResource.
	if postgresd == nil {
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

	// Block until the database reports state=running so apply completes only when the
	// resource is actually usable (connection_string/hostname populated), matching mature DB
	// providers. The POST above only ACCEPTS the create; the backend reaches running minutes
	// later over many respirate hops. This is appended on top of the existing create flow:
	// the dispatch and initial mapping above are unchanged, and the config hydration below
	// runs against the now-running database.
	runningPg, waitDiags := r.waitForPostgresRunning(opCtx, ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), createTimeout, postgresResourceLogIdentifier(&state))
	if runningPg != nil {
		// Re-map the latest observed surface: on success the running response now carries
		// connection_string/hostname; on timeout the last creating snapshot keeps state in
		// sync with the server.
		resp.Diagnostics.Append(setPostgresStateResource(ctx, runningPg, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if waitDiags.HasError() {
		// Persist the partial (creating) state so a timed-out create is tracked and replaced
		// on the next apply rather than recreated into a name conflict, then surface the
		// timeout so Terraform taints the resource. ensurePostgresConfigKnown makes the still
		// unknown config maps concrete so the partial state is settable.
		ensurePostgresConfigKnown(&state)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		resp.Diagnostics.Append(waitDiags...)
		return
	}

	// Hydrate pg_config/pgbouncer_config from the dedicated config endpoint (the create
	// response does not carry them), exactly as Read does, for both the primary and the
	// read-replica path. Without this an omitted-config create leaves them at the planned
	// unknown and Terraform rejects the apply. The GET is best-effort (it can 500 at
	// creating-state, before representative_server is ready), so default any still-unknown
	// map to empty afterward: an omitted-config create carries no overrides, so empty is the
	// server's user_config and the apply converges to a known value either way.
	resp.Diagnostics.Append(r.hydratePostgresConfig(opCtx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ensurePostgresConfigKnown(&state)

	// hydratePostgresConfig is best-effort and swallows transport errors, so a deadline that
	// elapsed during the config read-back would otherwise return success; surface it as the
	// create timeout (or a parent cancellation) and persist the partial state so the resource is
	// tainted, keeping the whole create bounded by timeouts.create.
	if opd := postgresOpContextDiags(opCtx, ctx, "creating", "create", postgresResourceLogIdentifier(&state), createTimeout); opd != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		resp.Diagnostics.Append(opd...)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// adoptCreatedPostgresOnTimeout recovers from a create whose dispatch POST was bounded out by
// opCtx (timeouts.create elapsed, or the provider context was cancelled) AFTER the server may
// have already accepted the create. A POST that timed out client-side does not prove the
// database was not created, so a bounded best-effort lookup on the parent context adopts an
// already-created database into state, leaving the timed-out resource TRACKED (and replaced on
// the next apply, since the caller still appends the tainting timeout) instead of orphaned into
// a name conflict. The lookup RETRIES over postgresAdoptLookupBudget rather than firing once, so
// a transient GET failure or a brief post-accept visibility lag does not abandon a real database;
// it persists on the first confirmed 200. It persists nothing when the parent context is already
// cancelled (no reachable server, and a plan-built partial state is unsettable: its computed
// fields are unknown) or when the budget elapses without a 200 (treated as absence; a clean
// recreate is correct, and Read now drops a 404 from state so no phantom entry lingers). The
// budget runs off parentCtx, so Create takes a bounded grace past timeouts.create here rather
// than re-extending it indefinitely.
func (r *postgresResource) adoptCreatedPostgresOnTimeout(parentCtx context.Context, state *resource_postgres.PostgresModel, resp *resource.CreateResponse) {
	if parentCtx.Err() != nil {
		return
	}
	budgetCtx, cancel := context.WithTimeout(parentCtx, postgresAdoptLookupBudget)
	defer cancel()
	for {
		postgresResp, err := r.uc.client.GetPostgresDatabaseDetailsWithResponse(budgetCtx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
		if err == nil && postgresResp.StatusCode() == http.StatusOK && postgresResp.JSON200 != nil {
			if setPostgresStateResource(parentCtx, postgresResp.JSON200, state).HasError() {
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

// createPostgresPrimary runs the normal create (POST .../postgres/{name}). The
// create body carries size/storage_size and the optional ha_type/version; the
// parent-inherited attributes that ConflictsWith parent are all primary inputs here.
func (r *postgresResource) createPostgresPrimary(ctx context.Context, state *resource_postgres.PostgresModel) (*ubicloud_client.PostgresDatabase, diag.Diagnostics) {
	var diags diag.Diagnostics
	// size is computed_optional so a read replica can omit it; a primary cannot. Catch
	// a missing/empty size here (e.g. an interpolated size that resolved empty, which
	// the plan-time guard defers as unknown) rather than posting an empty size and
	// failing with an opaque server error.
	if state.Size.IsNull() || state.Size.IsUnknown() || state.Size.ValueString() == "" {
		diags.AddError(
			"Missing size for primary postgres database",
			fmt.Sprintf("size is required to create a primary postgres database: %s", postgresResourceLogIdentifier(state)),
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
	// Wire the remaining create inputs so a value set in config reaches the server instead of
	// being silently dropped to a default. flavor is read back (stays Computed); tags is read
	// back too; restrict_by_default and private_subnet_name are create-only and write-only.
	// Each is nil-guarded so an unset/unknown attribute is omitted from the request body.
	if state.Flavor.ValueString() != "" {
		body.Flavor = state.Flavor.ValueStringPointer()
	}
	if !state.RestrictByDefault.IsNull() && !state.RestrictByDefault.IsUnknown() {
		body.RestrictByDefault = state.RestrictByDefault.ValueBoolPointer()
	}
	if state.PrivateSubnetName.ValueString() != "" {
		body.PrivateSubnetName = state.PrivateSubnetName.ValueStringPointer()
	}
	// Transmit the user config maps so a create-with-config lands the overrides server-side;
	// without this the backend keeps its defaults and the config read-back in Read would
	// drift state away from the plan. Unset/unknown maps are omitted (postgresConfigToBody).
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
		// The database is gone server-side (deleted out of band, or a delete that finished after
		// its wait timed out). Drop it from state so the next plan converges (recreate, or nothing
		// to destroy) instead of erroring and wedging a phantom resource in state until a manual
		// state rm. This is the idiomatic drift handling for a 404 on Read.
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

	diags := setPostgresStateResource(ctx, postgresResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.hydratePostgresConfig(ctx, projectID, location, name, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// hydratePostgresConfig reads the user config maps from the dedicated config endpoint and
// overlays them onto state. The detailed GET does not carry pg_config/pgbouncer_config, so
// without this read-back resource state is not authoritative for them and a declarative
// config replace cannot compute key deletions. It is best-effort: at creating-state the
// config GET can fail (it derives default_pg_config from representative_server, which may
// not exist yet), so a transport error or non-200 leaves the prior values untouched rather
// than failing the whole Read. Only a genuine map-decode error surfaces as a diagnostic.
func (r *postgresResource) hydratePostgresConfig(ctx context.Context, projectID, location, name string, state *resource_postgres.PostgresModel) diag.Diagnostics {
	var diags diag.Diagnostics
	configResp, err := r.uc.client.GetPostgresDatabaseConfigWithResponse(ctx, projectID, location, name)
	if err != nil {
		tflog.Debug(ctx, fmt.Sprintf("Skipping postgres config hydration (transport error): %s: %s", name, err.Error()))
		return diags
	}
	if configResp.StatusCode() != http.StatusOK || configResp.JSON200 == nil {
		tflog.Debug(ctx, fmt.Sprintf("Skipping postgres config hydration (status %d): %s", configResp.StatusCode(), name))
		return diags
	}

	return applyPostgresConfigToState(ctx, configResp.JSON200, state)
}

// ensurePostgresConfigKnown defaults a still-unknown pg_config/pgbouncer_config to an empty
// known map. Create hydrates config from the best-effort GET .../config; when that read is
// skipped (a non-200 at creating-state) an omitted-config map stays unknown, which Terraform
// rejects after apply. Only an unknown map is touched, so a user-set value and a hydrated
// value are left intact; the empty map matches the server's user_config when nothing was sent.
func ensurePostgresConfigKnown(state *resource_postgres.PostgresModel) {
	empty := types.MapValueMust(types.StringType, map[string]attr.Value{})
	if state.PgConfig.IsUnknown() {
		state.PgConfig = empty
	}
	if state.PgbouncerConfig.IsUnknown() {
		state.PgbouncerConfig = empty
	}
}

// Update orchestrates the in-place mutable set, one endpoint per attribute group. The
// create-only immutables (flavor, parent, restrict_by_default, private_subnet_name,
// project_id, location) carry RequiresReplace, so Terraform replaces for them and they
// never reach Update. maintenance_window_start_at is a computed read field (set through
// the dedicated set-maintenance-window operation, not a declarative attribute), so it is
// not part of the dispatch. version -> the imperative one-major upgrade (POST .../upgrade,
// server-chosen target) is dispatched here, but ONLY on its own: it is rejected when
// combined with any other mutable change, since it is irreversible and a later failure
// would strand it. The remaining set maps as: {size, storage_size, ha_type, tags} -> PATCH,
// {pg_config, pgbouncer_config} -> config merge (only the changed map, so the companion is
// never wiped), name -> rename. All calls key off project_id/location/NAME; rename runs LAST
// so the content mutations address the stable old name and the name flip is the final step.
func (r *postgresResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state resource_postgres.PostgresModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	logID := postgresResourceLogIdentifier(&state)

	projectID := state.ProjectId.ValueString()
	location := state.Location.ValueString()
	oldName := state.Name.ValueString()
	dispatched := false

	// version -> imperative one-major upgrade (POST .../upgrade), not a PATCH field. Handle
	// it first so a rejected jump aborts before any other mutation lands (no half-apply).
	versionChanged := postgresAttrChanged(plan.Version, state.Version)
	if versionChanged {
		if summary, detail := postgresVersionUpgradeError(plan.Version.ValueString(), state.Version.ValueString()); summary != "" {
			resp.Diagnostics.AddError(summary, fmt.Sprintf("%s: %s", detail, logID))
			return
		}
		// A version upgrade is irreversible and isolated (ubi exposes it as `ubi pg upgrade`,
		// not a modify option). Refuse to combine it with other mutations: dispatching the
		// upgrade then erroring on a later PATCH/config/rename would strand an irreversible
		// upgrade with state unreconciled. Reject before any call (no half-apply).
		if postgresNonVersionMutableChanged(&plan, &state) {
			resp.Diagnostics.AddError(
				"Postgres version upgrade must be applied on its own",
				fmt.Sprintf("A major version upgrade cannot be combined with other changes (size, storage_size, ha_type, tags, pg_config, pgbouncer_config, name); apply the version change separately: %s.", logID))
			return
		}
		if postgresUpgradeInFlight(&plan, &state) {
			// An upgrade toward the planned version is already pending. A second POST would be
			// rejected by the backend convergence precheck, so do not re-dispatch; the re-read
			// + override below reconcile the plan with the in-flight target_version. Surface a
			// FAILED upgrade rather than masking it as perpetual progress.
			resp.Diagnostics.Append(r.checkPostgresUpgradeFailed(ctx, projectID, location, oldName, logID)...)
			if resp.Diagnostics.HasError() {
				return
			}
		} else {
			tflog.Debug(ctx, fmt.Sprintf("Upgrading postgres database: %s", logID))
			resp.Diagnostics.Append(r.applyPostgresUpgrade(ctx, projectID, location, oldName, logID)...)
			if resp.Diagnostics.HasError() {
				return
			}
		}
		dispatched = true
	}

	patchBody, patchChanged, diags := postgresPatchBody(ctx, &plan, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if patchChanged {
		tflog.Debug(ctx, fmt.Sprintf("Patching postgres database: %s", logID))
		resp.Diagnostics.Append(r.applyPostgresPatch(ctx, projectID, location, oldName, patchBody, logID)...)
		if resp.Diagnostics.HasError() {
			return
		}
		dispatched = true
	}

	var configResult *ubicloud_client.PostgresConfig
	configBody, configChanged, configDiags := postgresConfigPatchBody(ctx, &plan, &state)
	resp.Diagnostics.Append(configDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if configChanged {
		tflog.Debug(ctx, fmt.Sprintf("Replacing postgres database config: %s", logID))
		cfg, configDiags := r.applyPostgresConfigMerge(ctx, projectID, location, oldName, configBody, logID)
		resp.Diagnostics.Append(configDiags...)
		if resp.Diagnostics.HasError() {
			return
		}
		configResult = cfg
		dispatched = true
	}

	finalName := oldName
	if postgresAttrChanged(plan.Name, state.Name) {
		newName := plan.Name.ValueString()
		tflog.Debug(ctx, fmt.Sprintf("Renaming postgres database: %s -> %s", logID, newName))
		resp.Diagnostics.Append(r.applyPostgresRename(ctx, projectID, location, oldName, newName, logID)...)
		if resp.Diagnostics.HasError() {
			return
		}
		finalName = newName
		dispatched = true
	}

	// No mutable change reached Update (e.g. a refresh that only recomputed invariant
	// computeds): persist the planned state without a server round trip.
	if !dispatched {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	// Re-read under the final name so state reflects the server. Start from the plan so
	// plan-owned fields the detailed GET does not return (pg_config, pgbouncer_config,
	// storage_size) survive, then overlay the read surface.
	postgresResp, err := r.uc.client.GetPostgresDatabaseDetailsWithResponse(ctx, projectID, location, finalName)
	if err != nil {
		r.persistRenamedNameOnError(ctx, oldName, finalName, resp)
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading postgres database after update: %s", logID),
			err.Error(),
		)
		return
	}
	if postgresResp.StatusCode() != http.StatusOK {
		r.persistRenamedNameOnError(ctx, oldName, finalName, resp)
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading postgres database after update",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), logID, postgresResp.Body))
		return
	}

	model := plan
	resp.Diagnostics.Append(setPostgresStateResource(ctx, postgresResp.JSON200, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The config PATCH returns the authoritative post-merge server config; overlay it so
	// state mirrors the server exactly even when prior state was stale and the request
	// therefore omitted a tombstone for a key the server still holds. model started from
	// plan, so when config did not change the plan value (carried forward by
	// UseStateForUnknown) already matches and configResult is nil (a no-op overlay).
	resp.Diagnostics.Append(applyPostgresConfigToState(ctx, configResult, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// size mirrors the actual vm_size in the read surface, which lags a requested resize
	// until convergence. The planned size is known during apply and the framework requires
	// the post-apply state to equal it, so keep the request here (target_vm_size carries
	// the in-flight target); a later refresh resurfaces the actual vm_size as a benign
	// pending diff until the resize converges.
	if !plan.Size.IsUnknown() {
		model.Size = plan.Size
	}

	// version, like size, mirrors the actual major in the read surface, which lags a
	// requested upgrade until convergence. Hold the requested version (target_version
	// carries the in-flight target) so the post-apply state equals the plan; a later refresh
	// resurfaces the lagging actual as a benign pending diff until the upgrade converges.
	if versionChanged && !plan.Version.IsUnknown() {
		model.Version = plan.Version
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

// persistRenamedNameOnError records the new name in state when the rename already
// succeeded on the server but the follow-up read failed. Without it state keeps the OLD
// name, a later refresh 404s on it, and Terraform plans a spurious recreate of a row that
// actually exists under the new name.
func (r *postgresResource) persistRenamedNameOnError(ctx context.Context, oldName, finalName string, resp *resource.UpdateResponse) {
	if finalName == oldName {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), finalName)...)
}

func (r *postgresResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resource_postgres.PostgresModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Bound the WHOLE delete (the DELETE request and the wait-until-gone poll) by
	// timeouts.delete, so a stuck request is cancelled at the deadline rather than hanging on
	// the provider-wide context. opCtx is used for every network call.
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
		// A DELETE failure caused by the operation deadline or a parent cancellation is part of
		// the bounded-delete behavior; report it as such rather than as a generic transport error.
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

	// A 404 means the database was already gone; nothing to wait for.
	if postgresResp.StatusCode() == http.StatusNotFound {
		return
	}

	// Block until the row is actually gone (GET 404). The DELETE above only ACCEPTS the
	// teardown; the backend drains it over many respirate hops (destroy + EC2/EBS/IAM), so a
	// returning destroy that did not wait would leave the resource still present.
	resp.Diagnostics.Append(r.waitForPostgresDeleted(opCtx, ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), deleteTimeout, postgresResourceLogIdentifier(&state))...)
}

// waitForPostgresRunning polls the detailed GET until the database reports state=running, the
// operation context is cancelled, or its deadline (timeouts.create) elapses. opCtx carries the
// operation-scoped timeout and is used for every GET and every sleep, so a stuck read is bounded
// by the deadline rather than the provider-wide context. parentCtx is the original provider
// context, consulted only to classify a termination: a parent cancellation or parent deadline is
// reported as a cancellation (raising timeouts.create could not help), the operation timeout as a
// timeout. Postgres has no distinct terminal failure state in display_state (unavailable is
// recoverable, not terminal), so a genuinely stuck create surfaces as the timeout rather than a
// false early failure. It returns the most recently observed database so the caller can map the
// full read surface on success or persist a partial snapshot on timeout; diags carries any error.
func (r *postgresResource) waitForPostgresRunning(opCtx, parentCtx context.Context, projectID, location, name string, timeout time.Duration, logID string) (*ubicloud_client.PostgresDatabase, diag.Diagnostics) {
	var diags diag.Diagnostics
	var lastPg *ubicloud_client.PostgresDatabase
	for {
		postgresResp, err := r.uc.client.GetPostgresDatabaseDetailsWithResponse(opCtx, projectID, location, name)
		if err != nil {
			if opCtx.Err() != nil {
				diags.Append(postgresRunningWaitContextDiags(parentCtx, logID, timeout, lastPg)...)
				return lastPg, diags
			}
			diags.AddError(
				fmt.Sprintf("Error polling postgres database while waiting for it to become ready: %s", logID),
				err.Error())
			return lastPg, diags
		}
		if postgresResp.StatusCode() != http.StatusOK {
			diags.AddError(
				"Unexpected HTTP status code waiting for postgres database to become ready",
				fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), logID, postgresResp.Body))
			return lastPg, diags
		}
		// Only adopt a parseable body as the latest snapshot; a 200 with no JSON keeps the prior
		// snapshot (or nil) and falls through to another bounded poll instead of dereferencing nil.
		if postgresResp.JSON200 != nil {
			lastPg = postgresResp.JSON200
			if lastPg.State == postgresStateRunning {
				return lastPg, diags
			}
		}
		select {
		case <-opCtx.Done():
			diags.Append(postgresRunningWaitContextDiags(parentCtx, logID, timeout, lastPg)...)
			return lastPg, diags
		case <-time.After(postgresPollInterval):
		}
	}
}

// postgresRunningWaitContextDiags renders a terminated create wait as a diagnostic. When the
// parent (provider) context is the cause it is reported as a cancellation, since raising
// timeouts.create cannot help; otherwise the operation timeout fired and the message names the
// last observed state and the knob to raise.
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

// waitForPostgresDeleted polls the detailed GET until it 404s, the operation context is
// cancelled, or its deadline (timeouts.delete) elapses. opCtx carries the operation-scoped
// timeout and is used for every GET and sleep; parentCtx is consulted only to classify a
// termination (parent cancellation/deadline vs the operation timeout). The DELETE only accepts
// the teardown; this makes destroy block until the row is actually gone.
func (r *postgresResource) waitForPostgresDeleted(opCtx, parentCtx context.Context, projectID, location, name string, timeout time.Duration, logID string) diag.Diagnostics {
	var diags diag.Diagnostics
	for {
		postgresResp, err := r.uc.client.GetPostgresDatabaseDetailsWithResponse(opCtx, projectID, location, name)
		if err != nil {
			if opCtx.Err() != nil {
				diags.Append(postgresDeletedWaitContextDiags(parentCtx, logID, timeout)...)
				return diags
			}
			diags.AddError(
				fmt.Sprintf("Error polling postgres database while waiting for it to be deleted: %s", logID),
				err.Error())
			return diags
		}
		if postgresResp.StatusCode() == http.StatusNotFound {
			return diags
		}
		if postgresResp.StatusCode() != http.StatusOK {
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

// postgresDeletedWaitContextDiags renders a terminated delete wait: a parent (provider) context
// cause is reported as cancellation (raising timeouts.delete cannot help), otherwise the
// operation timeout fired.
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

// postgresOpContextDiags classifies an operation context that terminated at a create/delete step
// OTHER than the readiness/teardown poll (the dispatch request, config hydration, or the DELETE).
// It returns nil while opCtx is still live; a cancellation diagnostic when the parent (provider)
// context ended (raising timeouts.* could not help); otherwise the operation-timeout diagnostic.
// This keeps a deadline that lands on the first request, or that is swallowed by best-effort
// hydration, from surfacing as a bare transport error or, worse, as silent success. verb labels
// the step (e.g. "creating"); knob is the timeouts.* field to raise (e.g. "create").
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
	// parent is a create-time reference the user supplies (name or id); the response
	// returns the parent's canonical PATH, a different string. Fill it from the
	// response only when the caller has not already set it, so a user-supplied parent
	// round-trips without an inconsistent-result error or RequiresReplace churn (a
	// normal pg stays null; an imported replica gets the response path).
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

func postgresResourceLogIdentifier(state *resource_postgres.PostgresModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
