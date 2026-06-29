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
	"github.com/hashicorp/terraform-plugin-framework/types"
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
	body := ubicloud_client.CreateLocationFirewallRuleJSONRequestBody{
		Cidr: state.Cidr.ValueString(),
	}
	if state.PortRange.ValueString() != "" {
		body.PortRange = state.PortRange.ValueStringPointer()
	}

	tflog.Debug(ctx, fmt.Sprintf("Creating firewall rule: project_id=%s, location=%s, firewall_reference: %s", state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallReference.ValueString()))
	firewallRuleResp, err := r.uc.client.CreateLocationFirewallRuleWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallReference.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error creating firewall rule: project_id=%s, location=%s, firewall_reference: %s", state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallReference.ValueString()),
			err.Error(),
		)
		return
	}

	if firewallRuleResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code creating firewall rule",
			fmt.Sprintf("Received %s creating new firewall rule: project_id=%s, location=%s, firewall_reference=%s. Details: %s", firewallRuleResp.Status(), state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallReference.ValueString(), firewallRuleResp.Body))
		return
	}

	rule, err := firewallRuleResp.JSON200.AsFirewallRule()
	if err != nil {
		resp.Diagnostics.AddError(
			"Error parsing firewall rule response",
			err.Error(),
		)
		return
	}

	state.Id = types.StringValue(rule.Id)
	state.Cidr = types.StringValue(rule.Cidr)
	state.PortRange = normalizedPortRange(state.PortRange.ValueString(), rule.PortRange)
	state.Description = types.StringValue(rule.Description)
	state.Protocol = types.StringValue(string(rule.Protocol))

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *firewallRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_firewall_rule.FirewallRuleModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading firewall rule: %s", firewallRuleResourceLogIdentifier(&state)))
	firewallRuleResp, err := r.uc.client.GetLocationFirewallFirewallRuleDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallReference.ValueString(), state.Id.ValueString())
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

	state.Id = types.StringValue(firewallRuleResp.JSON200.Id)
	state.Cidr = types.StringValue(firewallRuleResp.JSON200.Cidr)
	state.PortRange = normalizedPortRange(state.PortRange.ValueString(), firewallRuleResp.JSON200.PortRange)
	state.Description = types.StringValue(firewallRuleResp.JSON200.Description)
	state.Protocol = types.StringValue(string(firewallRuleResp.JSON200.Protocol))

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
	firewallRuleResp, err := r.uc.client.DeleteLocationFirewallFirewallRuleWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallReference.ValueString(), state.Id.ValueString())
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

	if len(idParts) != 4 || idParts[0] == "" || idParts[1] == "" || idParts[2] == "" || idParts[3] == "" {
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			fmt.Sprintf("Expected import identifier with format: project_id,location,firewall_name,id. Got: %q", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), idParts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("location"), idParts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("firewall_reference"), idParts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), idParts[3])...)
}

// normalizedPortRange returns the prior value if it is a config-form alias for
// the API's canonical value (e.g. "22..22" → "22"), so that state stays
// consistent with what the user wrote and no spurious diff is produced.
// In all other cases it returns the API's value.
func normalizedPortRange(prior, apiValue string) types.String {
	if prior != "" && normalizePortRange(prior) == apiValue {
		return types.StringValue(prior)
	}
	return types.StringValue(apiValue)
}

func normalizePortRange(s string) string {
	parts := strings.SplitN(s, "..", 2)
	if len(parts) == 2 && parts[0] == parts[1] {
		return parts[0]
	}
	return s
}

func firewallRuleResourceLogIdentifier(state *resource_firewall_rule.FirewallRuleModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, firewall_reference=%s, rule_id=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallReference.ValueString(), state.Id.ValueString())
}
