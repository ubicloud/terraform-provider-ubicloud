package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_firewall_rule"
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
	_ datasource.DataSource              = &firewallRuleDataSource{}
	_ datasource.DataSourceWithConfigure = &firewallRuleDataSource{}
)

func NewFirewallRuleDataSource() datasource.DataSource {
	return &firewallRuleDataSource{}
}

type firewallRuleDataSource struct {
	uc *UbicloudClient
}

func (d *firewallRuleDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *firewallRuleDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_rule"
}

func (d *firewallRuleDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = datasource_firewall_rule.FirewallRuleDataSourceSchema(ctx)
	resp.Schema.Description = "Get information about a Ubicloud firewall rule."
}

func (d *firewallRuleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state datasource_firewall_rule.FirewallRuleModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading firewall rule: %s", firewallRuleDataSourceLogIdentifier(&state)))
	firewallRuleResp, err := d.uc.client.GetFirewallRuleDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallName.ValueString(), state.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading firewall rule: %s", firewallRuleDataSourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallRuleResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading firewall rule",
			fmt.Sprintf("Received %s reading firewall rule: %s. Details: %s", firewallRuleResp.Status(), firewallRuleDataSourceLogIdentifier(&state), firewallRuleResp.Body))
		return
	}

	assignStr(firewallRuleResp.JSON200.Id, &state.Id)
	assignStr(firewallRuleResp.JSON200.Cidr, &state.Cidr)
	assignStr(firewallRuleResp.JSON200.PortRange, &state.PortRange)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func GetFirewallRulesState(ctx context.Context, firewallRules *[]ubicloud_client.FirewallRule) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics
	// Firewalls
	var firewallRulesValues []datasource_vm.FirewallRulesValue
	firewallRulesValue := datasource_vm.FirewallRulesValue{}
	if firewallRules != nil && len(*firewallRules) > 0 {
		for _, r := range *firewallRules {
			fr := datasource_vm.NewFirewallRulesValueMust(firewallRulesValue.AttributeTypes(ctx), map[string]attr.Value{
				"id":         types.StringPointerValue(r.Id),
				"cidr":       types.StringPointerValue(r.Cidr),
				"port_range": types.StringPointerValue(r.PortRange),
			})
			firewallRulesValues = append(firewallRulesValues, fr)
		}
	} else {
		firewallRulesValues = []datasource_vm.FirewallRulesValue{}
	}

	fwRules, diag := types.ListValueFrom(ctx, firewallRulesValue.Type(ctx), firewallRulesValues)
	diags.Append(diag...)
	if diags.HasError() {
		return basetypes.NewListUnknown(firewallRulesValue.Type(ctx)), diags
	}

	return fwRules, diags
}

func firewallRuleDataSourceLogIdentifier(state *datasource_firewall_rule.FirewallRuleModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, firewall_name=%s, rule_id=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallName.ValueString(), state.Id.ValueString())
}
