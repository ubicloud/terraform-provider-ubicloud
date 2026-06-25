package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_private_subnet"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &privateSubnetResource{}
	_ resource.ResourceWithConfigure   = &privateSubnetResource{}
	_ resource.ResourceWithImportState = &privateSubnetResource{}
)

func NewPrivateSubnetResource() resource.Resource {
	return &privateSubnetResource{}
}

type privateSubnetResource struct {
	uc *UbicloudClient
}

func (r *privateSubnetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *privateSubnetResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_private_subnet"
}

func (r *privateSubnetResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_private_subnet.PrivateSubnetResourceSchema(ctx)
	resp.Schema.Description = "Provides a Ubicloud PrivateSubnet resource. This can be used to create and delete private subnets."

}

func (r *privateSubnetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_private_subnet.PrivateSubnetModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := ubicloud_client.CreatePrivateSubnetJSONRequestBody{}
	if state.FirewallId.ValueString() != "" {
		body.FirewallId = state.FirewallId.ValueStringPointer()
	}

	tflog.Debug(ctx, fmt.Sprintf("Creating private subnet: %s", privateSubnetResourceLogIdentifier(&state)))
	privateSubnetResp, err := r.uc.client.CreatePrivateSubnetWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error creating private subnet: %s", privateSubnetResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if privateSubnetResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code creating privateSubnet",
			fmt.Sprintf("Received %s for private subnet: %s. Details: %s", privateSubnetResp.Status(), privateSubnetResourceLogIdentifier(&state), privateSubnetResp.Body))
		return
	}

	diags := setPrivateSubnetStateResource(ctx, privateSubnetResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *privateSubnetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_private_subnet.PrivateSubnetModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading private subnet: %s", privateSubnetResourceLogIdentifier(&state)))
	privateSubnetResp, err := r.uc.client.GetPrivateSubnetDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading private subnet: %s", privateSubnetResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if privateSubnetResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading private subnet",
			fmt.Sprintf("Received %s for private subnet %s. Details: %s", privateSubnetResp.Status(), privateSubnetResourceLogIdentifier(&state), privateSubnetResp.Body))
		return
	}

	diags := setPrivateSubnetStateResource(ctx, privateSubnetResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *privateSubnetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state resource_private_subnet.PrivateSubnetModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddError(
		"Update of privateSubnet is not supported",
		fmt.Sprintf("Cannot update private subnet: %s", privateSubnetResourceLogIdentifier(&state)))
}

func (r *privateSubnetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resource_private_subnet.PrivateSubnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Deleting private subnet: %s", privateSubnetResourceLogIdentifier(&state)))
	privateSubnetResp, err := r.uc.client.DeletePrivateSubnetWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error deleting private subnet: %s", privateSubnetResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if privateSubnetResp.StatusCode() != http.StatusNoContent && privateSubnetResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code deleting private subnet",
			fmt.Sprintf("Received %s for private subnet: %s. Details: %s", privateSubnetResp.Status(), privateSubnetResourceLogIdentifier(&state), privateSubnetResp.Body))
		return
	}
}

func (r *privateSubnetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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

func setPrivateSubnetStateResource(ctx context.Context, ps *ubicloud_client.PrivateSubnet, state *resource_private_subnet.PrivateSubnetModel) diag.Diagnostics {
	state.Id = types.StringValue(ps.Id)
	state.Net4 = types.StringValue(ps.Net4)
	state.Net6 = types.StringValue(ps.Net6)
	state.State = types.StringValue(ps.State)

	nicsListValue, diags := GetNicsState(ctx, ps.Nics)
	if diags.HasError() {
		return diags
	}
	state.Nics = nicsListValue

	firewallsListValue, diagsFw := getFirewallsStateResource(ctx, ps.Firewalls)
	diags.Append(diagsFw...)
	if diags.HasError() {
		return diags
	}

	state.Firewalls = firewallsListValue
	return diags
}

func getFirewallsStateResource(ctx context.Context, firewalls []ubicloud_client.Firewall) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	firewallsValue := resource_private_subnet.FirewallsValue{}
	var firewallsValues []resource_private_subnet.FirewallsValue
	firewallRulesValue := resource_private_subnet.FirewallRulesValue{}
	if len(firewalls) > 0 {
		for _, f := range firewalls {
			var firewallRulesValues []resource_private_subnet.FirewallRulesValue
			for _, r := range f.FirewallRules {
				fr := resource_private_subnet.NewFirewallRulesValueMust(firewallRulesValue.AttributeTypes(ctx), map[string]attr.Value{
					"id":          types.StringValue(r.Id),
					"cidr":        types.StringValue(r.Cidr),
					"port_range":  types.StringValue(r.PortRange),
					"description": types.StringValue(r.Description),
					"protocol":    types.StringValue(string(r.Protocol)),
				})
				firewallRulesValues = append(firewallRulesValues, fr)
			}
			if firewallRulesValues == nil {
				firewallRulesValues = []resource_private_subnet.FirewallRulesValue{}
			}
			fwRules, diag := types.ListValueFrom(ctx, firewallRulesValue.Type(ctx), firewallRulesValues)
			diags.Append(diag...)
			if diags.HasError() {
				return types.ListUnknown(firewallsValue.Type(ctx)), diags
			}
			fw := resource_private_subnet.NewFirewallsValueMust(firewallsValue.AttributeTypes(ctx), map[string]attr.Value{
				"id":             types.StringValue(f.Id),
				"location":       types.StringValue(f.Location),
				"name":           types.StringValue(f.Name),
				"description":    types.StringValue(f.Description),
				"firewall_rules": fwRules,
			})
			firewallsValues = append(firewallsValues, fw)
		}
	} else {
		firewallsValues = []resource_private_subnet.FirewallsValue{}
	}
	firewallsListValue, diag := types.ListValueFrom(ctx, firewallsValue.Type(ctx), firewallsValues)
	diags.Append(diag...)
	if diags.HasError() {
		return types.ListUnknown(firewallsValue.Type(ctx)), diags
	}
	return firewallsListValue, diags
}

func privateSubnetResourceLogIdentifier(state *resource_private_subnet.PrivateSubnetModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
