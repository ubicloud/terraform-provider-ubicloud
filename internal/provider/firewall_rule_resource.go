package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall_rule"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &firewallRuleResource{}
	_ resource.ResourceWithConfigure   = &firewallRuleResource{}
	_ resource.ResourceWithImportState = &firewallRuleResource{}
)

func NewFirewallRuleResource() resource.Resource {
	return &firewallRuleResource{}
}

type firewallRuleResource struct {
	uc *UbicloudClient
}

func (r *firewallRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *firewallRuleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_rule"
}

func (r *firewallRuleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_firewall_rule.FirewallRuleResourceSchema(ctx)
	resp.Schema.Description = "Provides a Ubicloud FirewallRule resource. This can be used to create and delete firewall rules."
}

func (r *firewallRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_firewall_rule.FirewallRuleModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := ubicloud_client.CreateFirewallRuleJSONRequestBody{
		Cidr: state.Cidr.ValueString(),
	}
	if state.PortRange.ValueString() != "" {
		body.PortRange = state.PortRange.ValueStringPointer()
	}

	tflog.Debug(ctx, fmt.Sprintf("Creating firewall rule: project_id=%s, firewall_id: %s", state.ProjectId.ValueString(), state.FirewallId.ValueString()))
	firewallRuleResp, err := r.uc.client.CreateFirewallRuleWithResponse(ctx, state.ProjectId.ValueString(), state.FirewallId.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error creating firewall rule: project_id=%s, firewall_id: %s", state.ProjectId.ValueString(), state.FirewallId.ValueString()),
			err.Error(),
		)
		return
	}

	if firewallRuleResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code creating firewall rule",
			fmt.Sprintf("Received %s creating new firewall rule: project_id=%s, firewall_id=%s. Details: %s", firewallRuleResp.Status(), state.ProjectId.ValueString(), state.FirewallId.ValueString(), firewallRuleResp.Body))
		return
	}

	assignStr(firewallRuleResp.JSON200.Id, &state.Id)
	assignStr(firewallRuleResp.JSON200.Cidr, &state.Cidr)
	assignStr(firewallRuleResp.JSON200.PortRange, &state.PortRange)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *firewallRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_firewall_rule.FirewallRuleModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading firewall rule: %s", firewallRuleResourceLogIdentifier(&state)))
	firewallRuleResp, err := r.uc.client.GetFirewallRuleDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.FirewallId.ValueString(), state.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading firewall rule: %s", firewallRuleResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallRuleResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading firewall rule",
			fmt.Sprintf("Received %s for firewall rule: %s. Details: %s", firewallRuleResp.Status(), firewallRuleResourceLogIdentifier(&state), firewallRuleResp.Body))
		return
	}

	assignStr(firewallRuleResp.JSON200.Id, &state.Id)
	assignStr(firewallRuleResp.JSON200.Cidr, &state.Cidr)
	assignStr(firewallRuleResp.JSON200.PortRange, &state.PortRange)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *firewallRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state resource_firewall_rule.FirewallRuleModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddError(
		"Update of firewall rule is not supported",
		fmt.Sprintf("Cannot update firewall rule: %s", firewallRuleResourceLogIdentifier(&state)))
}

func (r *firewallRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resource_firewall_rule.FirewallRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Deleting firewall rule: %s", firewallRuleResourceLogIdentifier(&state)))
	firewallRuleResp, err := r.uc.client.DeleteFirewallRuleWithResponse(ctx, state.ProjectId.ValueString(), state.FirewallId.ValueString(), state.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error deleting firewall rule: %s", firewallRuleResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallRuleResp.StatusCode() != http.StatusNoContent && firewallRuleResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code deleting firewallRule",
			fmt.Sprintf("Received %s deleting firewall rule: %s. Details: %s", firewallRuleResp.Status(), fmt.Sprintf("Deleting firewall rule: %s", firewallRuleResourceLogIdentifier(&state)), firewallRuleResp.Body))
		return
	}
}

func (r *firewallRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	idParts := strings.Split(req.ID, ",")

	if len(idParts) != 3 || idParts[0] == "" || idParts[1] == "" || idParts[2] == "" {
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			fmt.Sprintf("Expected import identifier with format: project_id,firewall_id,id. Got: %q", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), idParts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("firewall_id"), idParts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), idParts[2])...)
}

func firewallRuleResourceLogIdentifier(state *resource_firewall_rule.FirewallRuleModel) string {
	return fmt.Sprintf("project_id=%s, firewall_id=%s, rule_id=%s", state.ProjectId.ValueString(), state.FirewallId.ValueString(), state.Id.ValueString())
}
