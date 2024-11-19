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
	firewallResp, err := d.uc.client.GetFirewallDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Id.ValueString())
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

func GetFirewallsState(ctx context.Context, firewalls *[]ubicloud_client.Firewall) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics

	firewallsValue := datasource_vm.FirewallsValue{}
	var firewallsValues []datasource_vm.FirewallsValue
	firewallRulesValue := datasource_vm.FirewallRulesValue{}
	if *firewalls != nil && len(*firewalls) > 0 {
		for _, f := range *firewalls {

			fwRules, fwRulesDiag := GetFirewallRulesState(ctx, f.FirewallRules)
			diags.Append(fwRulesDiag...)
			if diags.HasError() {
				return basetypes.NewListUnknown(firewallRulesValue.Type(ctx)), diags
			}

			fw := datasource_vm.NewFirewallsValueMust(firewallsValue.AttributeTypes(ctx), map[string]attr.Value{
				"id":             types.StringValue(*f.Id),
				"location":       types.StringValue(*f.Location),
				"name":           types.StringValue(*f.Name),
				"description":    types.StringValue(*f.Description),
				"firewall_rules": fwRules,
			})
			firewallsValues = append(firewallsValues, fw)
		}
	} else {
		firewallsValues = []datasource_vm.FirewallsValue{}
	}
	firewallsListValue, diag := types.ListValueFrom(ctx, firewallsValue.Type(ctx), firewallsValues)
	diags.Append(diag...)
	if diags.HasError() {
		return basetypes.NewListUnknown(firewallRulesValue.Type(ctx)), diags
	}

	return firewallsListValue, diags
}

func firewallDataSourceLogIdentifier(state *datasource_firewall.FirewallModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, firewall_id=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Id.ValueString())
}
