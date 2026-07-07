package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
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

	// A non-JSON 200 (proxy interposition) leaves JSON200 nil; fail closed rather than nil-deref.
	if firewallResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Empty response creating firewall",
			fmt.Sprintf("the API returned no firewall body: %s", firewallResourceLogIdentifier(&state)),
		)
		return
	}

	state.Id = types.StringValue(firewallResp.JSON200.Id)
	state.Name = types.StringValue(firewallResp.JSON200.Name)
	state.Description = types.StringValue(firewallResp.JSON200.Description)

	firewallRulesListValue, fwRulesDiags := getFirewallRulesStateResource(ctx, firewallResp.JSON200.FirewallRules)
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
	firewallResp, err := r.uc.client.GetLocationFirewallDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading firewall: %s", firewallResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallResp.StatusCode() == http.StatusNotFound {
		// Gone server-side: drop from state so the next plan converges instead of erroring.
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

	// A non-JSON 200 leaves JSON200 nil; not a 404, so fail closed without RemoveResource.
	if firewallResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Empty response reading firewall",
			fmt.Sprintf("the API returned no firewall body: %s", firewallResourceLogIdentifier(&state)),
		)
		return
	}

	state.Name = types.StringValue(firewallResp.JSON200.Name)
	state.Location = types.StringValue(firewallResp.JSON200.Location)
	state.Description = types.StringValue(firewallResp.JSON200.Description)

	firewallRulesListValue, fwRulesDiags := getFirewallRulesStateResource(ctx, firewallResp.JSON200.FirewallRules)
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

func getFirewallRulesStateResource(ctx context.Context, firewallRules []ubicloud_client.FirewallRule) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	firewallRulesValue := resource_firewall.FirewallRulesValue{}
	var firewallRulesValues []resource_firewall.FirewallRulesValue
	if len(firewallRules) > 0 {
		for _, r := range firewallRules {
			fr := resource_firewall.NewFirewallRulesValueMust(firewallRulesValue.AttributeTypes(ctx), map[string]attr.Value{
				"id":          types.StringValue(r.Id),
				"cidr":        types.StringValue(r.Cidr),
				"port_range":  types.StringValue(r.PortRange),
				"description": types.StringValue(r.Description),
				"protocol":    types.StringValue(string(r.Protocol)),
			})
			firewallRulesValues = append(firewallRulesValues, fr)
		}
	} else {
		firewallRulesValues = []resource_firewall.FirewallRulesValue{}
	}

	fwRules, diag := types.ListValueFrom(ctx, firewallRulesValue.Type(ctx), firewallRulesValues)
	diags.Append(diag...)
	if diags.HasError() {
		return types.ListUnknown(firewallRulesValue.Type(ctx)), diags
	}

	return fwRules, diags
}

func firewallResourceLogIdentifier(state *resource_firewall.FirewallModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
