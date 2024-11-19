package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
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
	body := ubicloud_client.CreateFirewallJSONRequestBody{}
	if state.Description.ValueString() != "" {
		body.Description = state.Description.ValueStringPointer()
	}

	tflog.Debug(ctx, fmt.Sprintf("Creating firewall: project_id=%s", state.ProjectId.ValueString()))
	firewallResp, err := r.uc.client.CreateFirewallWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), body)
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

	assignStr(firewallResp.JSON200.Id, &state.Id)
	assignStr(firewallResp.JSON200.Name, &state.Name)
	assignStr(firewallResp.JSON200.Description, &state.Description)

	firewallRulesListValue, fwRulesDiags := GetFirewallRulesState(ctx, firewallResp.JSON200.FirewallRules)
	resp.Diagnostics.Append(fwRulesDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.FirewallRules = firewallRulesListValue

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *firewallResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_firewall.FirewallModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading firewall: %s", firewallResourceLogIdentifier(&state)))
	firewallResp, err := r.uc.client.GetFirewallDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading firewall: %s", firewallResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading firewall",
			fmt.Sprintf("Received %s for firewall: %s. Details: %s", firewallResp.Status(), firewallResourceLogIdentifier(&state), firewallResp.Body))
		return
	}

	assignStr(firewallResp.JSON200.Name, &state.Name)
	assignStr(firewallResp.JSON200.Location, &state.Location)
	assignStr(firewallResp.JSON200.Description, &state.Description)

	firewallRulesListValue, fwRulesDiags := GetFirewallRulesState(ctx, firewallResp.JSON200.FirewallRules)
	resp.Diagnostics.Append(fwRulesDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.FirewallRules = firewallRulesListValue

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
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
	firewallResp, err := r.uc.client.DeleteFirewallWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
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
