package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_private_subnet"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &privateSubnetDataSource{}
	_ datasource.DataSourceWithConfigure = &privateSubnetDataSource{}
)

func NewPrivateSubnetDataSource() datasource.DataSource {
	return &privateSubnetDataSource{}
}

type privateSubnetDataSource struct {
	uc *UbicloudClient
}

func (d *privateSubnetDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *privateSubnetDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_private_subnet"
}

func (d *privateSubnetDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = datasource_private_subnet.PrivateSubnetDataSourceSchema(ctx)
	resp.Schema.Description = "Get information about a Ubicloud private subnet."
}

func (d *privateSubnetDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state datasource_private_subnet.PrivateSubnetModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading private subnet: %s", privateSubnetDataSourceLogIdentifier(&state)))
	privateSubnetResp, err := d.uc.client.GetPrivateSubnetDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading private subnet: %s", privateSubnetDataSourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if privateSubnetResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading private subnet",
			fmt.Sprintf("Received %s for private subnet: %s. Details: %s", privateSubnetResp.Status(), privateSubnetDataSourceLogIdentifier(&state), privateSubnetResp.Body))
		return
	}

	// A non-JSON 200 (proxy interposition) leaves JSON200 nil; fail closed rather than nil-deref.
	if privateSubnetResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Empty response reading private subnet",
			fmt.Sprintf("the API returned no private subnet body: %s", privateSubnetDataSourceLogIdentifier(&state)),
		)
		return
	}

	diags := setPrivateSubnetStateDatasource(ctx, privateSubnetResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func setPrivateSubnetStateDatasource(ctx context.Context, ps *ubicloud_client.PrivateSubnet, state *datasource_private_subnet.PrivateSubnetModel) diag.Diagnostics {
	state.Id = types.StringValue(ps.Id)
	state.Net4 = types.StringValue(ps.Net4)
	state.Net6 = types.StringValue(ps.Net6)

	nicsListValue, diags := GetNicsState(ctx, ps.Nics)
	if diags.HasError() {
		return diags
	}
	state.Nics = nicsListValue

	firewallsListValue, diagsFw := getFirewallsStateDatasource(ctx, ps.Firewalls)
	diags.Append(diagsFw...)
	if diags.HasError() {
		return diags
	}

	state.Firewalls = firewallsListValue
	return diags
}

func GetNicsState(ctx context.Context, nics []ubicloud_client.Nic) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics
	nicsValue := datasource_private_subnet.NicsValue{}
	var nicsValues []datasource_private_subnet.NicsValue
	if len(nics) > 0 {
		for _, n := range nics {
			nv := datasource_private_subnet.NewNicsValueMust(nicsValue.AttributeTypes(ctx), map[string]attr.Value{
				"id":           types.StringValue(n.Id),
				"name":         types.StringValue(n.Name),
				"private_ipv4": types.StringValue(n.PrivateIpv4),
				"private_ipv6": types.StringValue(n.PrivateIpv6),
				"vm_name":      types.StringPointerValue(n.VmName),
			})
			nicsValues = append(nicsValues, nv)
		}
	} else {
		nicsValues = []datasource_private_subnet.NicsValue{}
	}

	nicsListValue, diag := types.ListValueFrom(ctx, nicsValue.Type(ctx), nicsValues)
	diags.Append(diag...)
	if diags.HasError() {
		return basetypes.NewListUnknown(nicsValue.Type(ctx)), diags
	}

	return nicsListValue, diags
}

func getFirewallsStateDatasource(ctx context.Context, firewalls []ubicloud_client.Firewall) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics
	firewallsValue := datasource_private_subnet.FirewallsValue{}
	var firewallsValues []datasource_private_subnet.FirewallsValue
	firewallRulesValue := datasource_private_subnet.FirewallRulesValue{}
	if len(firewalls) > 0 {
		for _, f := range firewalls {
			var firewallRulesValues []datasource_private_subnet.FirewallRulesValue
			for _, r := range f.FirewallRules {
				fr := datasource_private_subnet.NewFirewallRulesValueMust(firewallRulesValue.AttributeTypes(ctx), map[string]attr.Value{
					"id":          types.StringValue(r.Id),
					"cidr":        types.StringValue(r.Cidr),
					"port_range":  types.StringValue(r.PortRange),
					"description": types.StringValue(r.Description),
					"protocol":    types.StringValue(string(r.Protocol)),
				})
				firewallRulesValues = append(firewallRulesValues, fr)
			}
			if firewallRulesValues == nil {
				firewallRulesValues = []datasource_private_subnet.FirewallRulesValue{}
			}
			fwRules, diagFr := types.ListValueFrom(ctx, firewallRulesValue.Type(ctx), firewallRulesValues)
			diags.Append(diagFr...)
			if diags.HasError() {
				return basetypes.NewListUnknown(firewallsValue.Type(ctx)), diags
			}
			fw := datasource_private_subnet.NewFirewallsValueMust(firewallsValue.AttributeTypes(ctx), map[string]attr.Value{
				"id":             types.StringValue(f.Id),
				"location":       types.StringValue(f.Location),
				"name":           types.StringValue(f.Name),
				"description":    types.StringValue(f.Description),
				"firewall_rules": fwRules,
			})
			firewallsValues = append(firewallsValues, fw)
		}
	} else {
		firewallsValues = []datasource_private_subnet.FirewallsValue{}
	}
	firewallsListValue, diagFw := types.ListValueFrom(ctx, firewallsValue.Type(ctx), firewallsValues)
	diags.Append(diagFw...)
	if diags.HasError() {
		return basetypes.NewListUnknown(firewallsValue.Type(ctx)), diags
	}
	return firewallsListValue, diags
}

func privateSubnetDataSourceLogIdentifier(state *datasource_private_subnet.PrivateSubnetModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
