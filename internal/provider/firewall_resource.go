package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &firewallResource{}
	_ resource.ResourceWithConfigure   = &firewallResource{}
	_ resource.ResourceWithImportState = &firewallResource{}
)

func NewFirewallResource() resource.Resource {
	return &firewallResource{}
}

type firewallResource struct {
	uc *UbicloudClient
}

func (r *firewallResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *firewallResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall"
}

func (r *firewallResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_firewall.FirewallResourceSchema(ctx)
	resp.Schema.Description = "Provides a Ubicloud Firewall resource. This can be used to create and delete firewalls."
}

func (r *firewallResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_firewall.FirewallModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := ubicloud_client.CreateLocationFirewallJSONRequestBody{}
	if state.Description.ValueString() != "" {
		body.Description = state.Description.ValueStringPointer()
	}

	tflog.Debug(ctx, fmt.Sprintf("Creating firewall: project_id=%s", state.ProjectId.ValueString()))
	firewallResp, err := r.uc.client.CreateLocationFirewallWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error creating firewall: project_id=%s", state.ProjectId.ValueString()),
			err.Error(),
		)
		return
	}

	if firewallResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code creating firewall",
			fmt.Sprintf("Received %s creating new firewall: project_id=%s. Details: %s", firewallResp.Status(), state.ProjectId.ValueString(), firewallResp.Body))
		return
	}

	// createLocationFirewall returns the base Firewall shape (openapi: no
	// private_subnets); only getLocationFirewallDetails carries them. A firewall
	// created through the API is attached to no private subnet (helpers/firewall.rb
	// firewall_post only associates one on the web path), so its detailed view is the
	// base fields with an empty private_subnets list. Mapping it through the same
	// setter as Read makes the computed private_subnets known (an empty list) instead
	// of leaving it unknown, which otherwise fails apply with "invalid result object".
	created := &ubicloud_client.FirewallDetailed{
		Id:             firewallResp.JSON200.Id,
		Name:           firewallResp.JSON200.Name,
		Location:       firewallResp.JSON200.Location,
		Description:    firewallResp.JSON200.Description,
		FirewallRules:  firewallResp.JSON200.FirewallRules,
		PrivateSubnets: []ubicloud_client.PrivateSubnet{},
	}
	resp.Diagnostics.Append(setFirewallStateResource(ctx, created, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *firewallResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_firewall.FirewallModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading firewall: %s", firewallResourceLogIdentifier(&state)))
	firewallResp, err := r.uc.client.GetLocationFirewallDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading firewall: %s", firewallResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallResp.StatusCode() == http.StatusNotFound {
		// 404 means the firewall is gone server-side; drop it from state so the next plan
		// converges (recreate or no-op) instead of erroring on drift.
		tflog.Debug(ctx, fmt.Sprintf("Firewall not found, removing from state: %s", firewallResourceLogIdentifier(&state)))
		resp.State.RemoveResource(ctx)
		return
	}

	if firewallResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading firewall",
			fmt.Sprintf("Received %s for firewall: %s. Details: %s", firewallResp.Status(), firewallResourceLogIdentifier(&state), firewallResp.Body))
		return
	}

	resp.Diagnostics.Append(setFirewallStateResource(ctx, firewallResp.JSON200, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// setFirewallStateResource maps a detailed firewall response onto the resource model.
// project_id is not carried in the response and is preserved from the prior state/plan.
func setFirewallStateResource(ctx context.Context, fw *ubicloud_client.FirewallDetailed, state *resource_firewall.FirewallModel) diag.Diagnostics {
	state.Id = types.StringValue(fw.Id)
	state.Name = types.StringValue(fw.Name)
	state.Location = types.StringValue(fw.Location)
	state.Description = types.StringValue(fw.Description)

	firewallRulesListValue, diags := GetFirewallRulesState(ctx, fw.FirewallRules)
	if diags.HasError() {
		return diags
	}
	state.FirewallRules = firewallRulesListValue

	privateSubnetsListValue, psDiags := GetPrivateSubnetsState(ctx, fw.PrivateSubnets)
	diags.Append(psDiags...)
	if diags.HasError() {
		return diags
	}
	state.PrivateSubnets = privateSubnetsListValue

	return diags
}

func (r *firewallResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state resource_firewall.FirewallModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddError(
		"Update of firewall is not supported",
		fmt.Sprintf("Cannot update firewall: %s", firewallResourceLogIdentifier(&state)))
}

func (r *firewallResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resource_firewall.FirewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Deleting firewall: %s", firewallResourceLogIdentifier(&state)))
	firewallResp, err := r.uc.client.DeleteLocationFirewallWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error deleting firewall: %s", firewallResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallResp.StatusCode() != http.StatusNoContent && firewallResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code deleting firewall",
			fmt.Sprintf("Received %s deleting firewall: %s. Details: %s", firewallResp.Status(), firewallResourceLogIdentifier(&state), firewallResp.Body))
		return
	}
}

func (r *firewallResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	idParts := strings.Split(req.ID, ",")

	if len(idParts) != 3 || idParts[0] == "" || idParts[1] == "" || idParts[2] == "" {
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			fmt.Sprintf("Expected import identifier with format: project_id,location,name,id. Got: %q", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), idParts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("location"), idParts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), idParts[2])...)
}

func firewallResourceLogIdentifier(state *resource_firewall.FirewallModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
