package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_load_balancer"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &loadBalancerDataSource{}
	_ datasource.DataSourceWithConfigure = &loadBalancerDataSource{}
)

func NewLoadBalancerDataSource() datasource.DataSource {
	return &loadBalancerDataSource{}
}

type loadBalancerDataSource struct {
	uc *UbicloudClient
}

func (d *loadBalancerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *loadBalancerDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (d *loadBalancerDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = datasource_load_balancer.LoadBalancerDataSourceSchema(ctx)
	resp.Schema.Description = "Get information about a Ubicloud load balancer."
}

func (d *loadBalancerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state datasource_load_balancer.LoadBalancerModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading load balancer: %s", loadBalancerDataSourceLogIdentifier(&state)))
	loadBalancerResp, err := d.uc.client.GetLoadBalancerDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading load balancer: %s", loadBalancerDataSourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if loadBalancerResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading load balancer",
			fmt.Sprintf("Received %s for load balancer: %s. Details: %s", loadBalancerResp.Status(), loadBalancerDataSourceLogIdentifier(&state), loadBalancerResp.Body))
		return
	}

	diags := setLoadBalancerStateDatasource(ctx, loadBalancerResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func setLoadBalancerStateDatasource(ctx context.Context, lb *ubicloud_client.LoadBalancer, state *datasource_load_balancer.LoadBalancerModel) diag.Diagnostics {
	assignStr(lb.Id, &state.Id)
	assignStr(lb.Hostname, &state.Hostname)
	assignStr(lb.Location, &state.Location)
	assignStr(lb.Name, &state.Name)
	assignStr(lb.HealthCheckEndpoint, &state.HealthCheckEndpoint)
	assignInt(lb.SrcPort, &state.SrcPort)
	assignInt(lb.DstPort, &state.DstPort)
	assignStr(lb.Algorithm, &state.Algorithm)
	assignStr(lb.Subnet, &state.Subnet)

	vmsListValue, diags := GetVmsState(ctx, lb.Vms)
	if diags.HasError() {
		return diags
	}

	state.Vms = vmsListValue
	if len(state.Vms.Elements()) == 0 {
		state.Vms, _ = basetypes.NewListValue(vmsListValue.ElementType(ctx), vmsListValue.Elements())
	}

	return diag.Diagnostics{}
}

func loadBalancerDataSourceLogIdentifier(state *datasource_load_balancer.LoadBalancerModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}

func GetVmsState(ctx context.Context, vms *[]ubicloud_client.VmId) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics

	vmsValue := basetypes.NewStringValue("")
	var vmsValues []string

	vmsValues = append(vmsValues, *vms...)

	vmsListValue, diag := types.ListValueFrom(ctx, vmsValue.Type(ctx), vmsValues)
	diags.Append(diag...)
	if diags.HasError() {
		return basetypes.NewListUnknown(vmsListValue.Type(ctx)), diags
	}

	return vmsListValue, diags
}
