package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_firewall"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_vm"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &firewallDataSource{}
	_ datasource.DataSourceWithConfigure = &firewallDataSource{}
)

func NewFirewallDataSource() datasource.DataSource {
	return &firewallDataSource{}
}

type firewallDataSource struct {
	uc *UbicloudClient
}

func (d *firewallDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
	d.uc = &uc
}

func (d *firewallDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall"
}

func (d *firewallDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = datasource_firewall.FirewallDataSourceSchema(ctx)
	resp.Schema.Description = "Get information about a Ubicloud firewall."
}

func (d *firewallDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state datasource_firewall.FirewallModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading firewall: %s", firewallDataSourceLogIdentifier(&state)))
	firewallResp, err := d.uc.client.GetLocationFirewallDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading firewall: %s", firewallDataSourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading firewall",
			fmt.Sprintf("Received %s for firewall: %s. Details: %s", firewallResp.Status(), firewallDataSourceLogIdentifier(&state), firewallResp.Body))
		return
	}

	resp.Diagnostics.Append(setFirewallStateDatasource(ctx, firewallResp.JSON200, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// setFirewallStateDatasource maps a detailed firewall response onto the data source
// model, including the previously unmapped computed private_subnets list.
func setFirewallStateDatasource(ctx context.Context, fw *ubicloud_client.FirewallDetailed, state *datasource_firewall.FirewallModel) diag.Diagnostics {
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

// GetPrivateSubnetsState maps the detailed firewall response's private_subnets onto the
// generated nested objects. The backend serializes each attached subnet as a full
// PrivateSubnet (id, name, state, location, net4, net6, nics, and a recursive firewalls
// list). The recursive firewalls list is dropped from the schema (ubi fw show omits it
// too), so this mirrors the ubi CLI private-subnet field set: id, name, state, location,
// net4, net6, nics.
func GetPrivateSubnetsState(ctx context.Context, privateSubnets []ubicloud_client.PrivateSubnet) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics

	privateSubnetsValue := datasource_firewall.PrivateSubnetsValue{}
	privateSubnetsValues := make([]datasource_firewall.PrivateSubnetsValue, 0, len(privateSubnets))
	for _, ps := range privateSubnets {
		nicsValue := datasource_firewall.NicsValue{}
		nicsValues := make([]datasource_firewall.NicsValue, 0, len(ps.Nics))
		for _, n := range ps.Nics {
			nv := datasource_firewall.NewNicsValueMust(nicsValue.AttributeTypes(ctx), map[string]attr.Value{
				"id":           types.StringValue(n.Id),
				"name":         types.StringValue(n.Name),
				"private_ipv4": types.StringValue(n.PrivateIpv4),
				"private_ipv6": types.StringValue(n.PrivateIpv6),
				"vm_name":      types.StringPointerValue(n.VmName),
			})
			nicsValues = append(nicsValues, nv)
		}

		nicsListValue, nicsDiag := types.ListValueFrom(ctx, nicsValue.Type(ctx), nicsValues)
		diags.Append(nicsDiag...)
		if diags.HasError() {
			return basetypes.NewListUnknown(privateSubnetsValue.Type(ctx)), diags
		}

		psv := datasource_firewall.NewPrivateSubnetsValueMust(privateSubnetsValue.AttributeTypes(ctx), map[string]attr.Value{
			"id":       types.StringValue(ps.Id),
			"location": types.StringValue(ps.Location),
			"name":     types.StringValue(ps.Name),
			"net4":     types.StringValue(ps.Net4),
			"net6":     types.StringValue(ps.Net6),
			"nics":     nicsListValue,
			"state":    types.StringValue(ps.State),
		})
		privateSubnetsValues = append(privateSubnetsValues, psv)
	}

	privateSubnetsListValue, listDiag := types.ListValueFrom(ctx, privateSubnetsValue.Type(ctx), privateSubnetsValues)
	diags.Append(listDiag...)
	if diags.HasError() {
		return basetypes.NewListUnknown(privateSubnetsValue.Type(ctx)), diags
	}

	return privateSubnetsListValue, diags
}

func GetFirewallsState(ctx context.Context, firewalls []ubicloud_client.Firewall) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics

	firewallsValue := datasource_vm.FirewallsValue{}
	firewallsValues := make([]datasource_vm.FirewallsValue, 0, len(firewalls))
	firewallRulesValue := datasource_vm.FirewallRulesValue{}
	for _, f := range firewalls {

		fwRules, fwRulesDiag := GetFirewallRulesState(ctx, f.FirewallRules)
		diags.Append(fwRulesDiag...)
		if diags.HasError() {
			return basetypes.NewListUnknown(firewallRulesValue.Type(ctx)), diags
		}

		fw := datasource_vm.NewFirewallsValueMust(firewallsValue.AttributeTypes(ctx), map[string]attr.Value{
			"id":             types.StringValue(f.Id),
			"location":       types.StringValue(f.Location),
			"name":           types.StringValue(f.Name),
			"description":    types.StringValue(f.Description),
			"firewall_rules": fwRules,
		})
		firewallsValues = append(firewallsValues, fw)
	}
	firewallsListValue, diag := types.ListValueFrom(ctx, firewallsValue.Type(ctx), firewallsValues)
	diags.Append(diag...)
	if diags.HasError() {
		return basetypes.NewListUnknown(firewallRulesValue.Type(ctx)), diags
	}

	return firewallsListValue, diags
}

func firewallDataSourceLogIdentifier(state *datasource_firewall.FirewallModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, firewall_name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
