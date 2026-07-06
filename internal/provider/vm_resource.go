package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_vm"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &vmResource{}
	_ resource.ResourceWithConfigure   = &vmResource{}
	_ resource.ResourceWithImportState = &vmResource{}
)

func NewVmResource() resource.Resource {
	return &vmResource{}
}

type vmResource struct {
	uc *UbicloudClient
}

func (r *vmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *vmResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (r *vmResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_vm.VmResourceSchema(ctx)
	resp.Schema.Description = "Provides a Ubicloud VM resource. This can be used to create and delete VMs."
	// The API fills in defaults for these when not specified, so they must be Computed.
	resp.Schema.Attributes["boot_image"] = schema.StringAttribute{
		Optional:            true,
		Computed:            true,
		Description:         "Boot image of the VM",
		MarkdownDescription: "Boot image of the VM",
	}
	resp.Schema.Attributes["enable_ip4"] = schema.BoolAttribute{
		Optional:            true,
		Computed:            true,
		Description:         "Enable IPv4",
		MarkdownDescription: "Enable IPv4",
	}
	resp.Schema.Attributes["storage_size"] = schema.Int64Attribute{
		Optional:            true,
		Computed:            true,
		Description:         "Requested storage size in GiB",
		MarkdownDescription: "Requested storage size in GiB",
	}
}

func (r *vmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_vm.VmModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := ubicloud_client.CreateVMJSONRequestBody{
		PublicKey: state.PublicKey.ValueString(),
	}

	if state.Size.ValueString() != "" {
		body.Size = state.Size.ValueStringPointer()
	}
	if state.UnixUser.ValueString() != "" {
		body.UnixUser = state.UnixUser.ValueStringPointer()
	}
	if state.BootImage.ValueString() != "" {
		body.BootImage = state.BootImage.ValueStringPointer()
	}
	if state.EnableIp4.ValueBool() {
		body.EnableIp4 = state.EnableIp4.ValueBoolPointer()
	}
	if state.PrivateSubnetId.ValueString() != "" {
		body.PrivateSubnetId = state.PrivateSubnetId.ValueStringPointer()
	}
	if state.StorageSize.ValueInt64() != 0 {
		storageSize := int(state.StorageSize.ValueInt64())
		body.StorageSize = &storageSize
	}

	tflog.Debug(ctx, fmt.Sprintf("Creating vm: %s.", vmResourceLogIdentifier(&state)))
	vmResp, err := r.uc.client.CreateVMWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error creating vm: %s.", vmResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if vmResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code creating vm",
			fmt.Sprintf("Received %s for vm: %s. Details: %s", vmResp.Status(), vmResourceLogIdentifier(&state), vmResp.Body))
		return
	}

	// A non-JSON 200 (proxy interposition) leaves JSON200 nil; fail closed rather than nil-deref.
	if vmResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Empty response creating vm",
			fmt.Sprintf("the API returned no vm body: %s", vmResourceLogIdentifier(&state)),
		)
		return
	}

	setVmStateResource(vmResp.JSON200, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_vm.VmModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading vm: %s.", vmResourceLogIdentifier(&state)))
	vmResp, err := r.uc.client.GetVMDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading vm: %s.", vmResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if vmResp.StatusCode() == http.StatusNotFound {
		// Gone server-side: drop from state so the next plan converges instead of erroring.
		tflog.Debug(ctx, fmt.Sprintf("Vm not found, removing from state: %s", vmResourceLogIdentifier(&state)))
		resp.State.RemoveResource(ctx)
		return
	}

	if vmResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading vm",
			fmt.Sprintf("Received %s for vm: %s. Details: %s", vmResp.Status(), vmResourceLogIdentifier(&state), vmResp.Body))
		return
	}

	// A non-JSON 200 leaves JSON200 nil; not a 404, so fail closed without RemoveResource.
	if vmResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Empty response reading vm",
			fmt.Sprintf("the API returned no vm body: %s", vmResourceLogIdentifier(&state)),
		)
		return
	}

	setVmStateResource(vmResp.JSON200, &state)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state resource_vm.VmModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddError(
		"Update of vm is not supported",
		fmt.Sprintf("Cannot update vm: %s", vmResourceLogIdentifier(&state)))
}

func (r *vmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resource_vm.VmModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Deleting vm: %s.", vmResourceLogIdentifier(&state)))
	vmResp, err := r.uc.client.DeleteVMWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error deleting vm: %s.", vmResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if vmResp.StatusCode() != http.StatusNoContent && vmResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code deleting vm",
			fmt.Sprintf("Received %s for vm: %s. Details: %s", vmResp.Status(), vmResourceLogIdentifier(&state), vmResp.Body))
		return
	}
}

func (r *vmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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

func setVmStateResource(vmd *ubicloud_client.Vm, state *resource_vm.VmModel) {
	state.BootImage = types.StringValue(vmd.BootImage)
	state.EnableIp4 = types.BoolValue(vmd.Ip4Enabled)
	state.Gpu = types.StringPointerValue(vmd.Gpu)
	state.Location = types.StringValue(vmd.Location)
	state.Name = types.StringValue(vmd.Name)
	state.Size = types.StringValue(vmd.Size)
	state.StorageSize = types.Int64Value(int64(vmd.StorageSizeGib))
	state.UnixUser = types.StringValue(vmd.UnixUser)
	// init_script is not returned by the API; preserve any known value already
	// in state (user-supplied on create) and default to empty when unknown.
	if state.InitScript.IsNull() || state.InitScript.IsUnknown() {
		state.InitScript = types.StringValue("")
	}
}

func vmResourceLogIdentifier(state *resource_vm.VmModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
