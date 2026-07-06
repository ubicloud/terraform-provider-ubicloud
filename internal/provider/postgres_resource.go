package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &postgresResource{}
	_ resource.ResourceWithConfigure   = &postgresResource{}
	_ resource.ResourceWithImportState = &postgresResource{}
)

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
}

func (r *postgresResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_postgres.PostgresModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
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

	tflog.Debug(ctx, fmt.Sprintf("Creating postgres database: %s", postgresResourceLogIdentifier(&state)))
	postgresResp, err := r.uc.client.CreatePostgresDatabaseWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error creating postgres database: %s", postgresResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if postgresResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code creating postgres database",
			fmt.Sprintf("Received %s for postgres database: %s. Details: %s", postgresResp.Status(), postgresResourceLogIdentifier(&state), postgresResp.Body))
		return
	}

	diags := setPostgresStateResource(ctx, postgresResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *postgresResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_postgres.PostgresModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading postgres database: %s", postgresResourceLogIdentifier(&state)))
	postgresResp, err := r.uc.client.GetPostgresDatabaseDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading postgres database: %s", postgresResourceLogIdentifier(&state)),
			err.Error(),
		)
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

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
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

	tflog.Debug(ctx, fmt.Sprintf("Deleting postgres database: %s", postgresResourceLogIdentifier(&state)))
	postgresResp, err := r.uc.client.DeletePostgresDatabaseWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
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

func postgresResourceLogIdentifier(state *resource_postgres.PostgresModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
