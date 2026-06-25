package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_vm"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &vmDataSource{}
	_ datasource.DataSourceWithConfigure = &vmDataSource{}
)

func NewVmDataSource() datasource.DataSource {
	return &vmDataSource{}
}

type vmDataSource struct {
	uc *UbicloudClient
}

func (d *vmDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *vmDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (d *vmDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = datasource_vm.VmDataSourceSchema(ctx)
	resp.Schema.Description = "Get information about a Ubicloud virtual machine."
}

func (d *vmDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state datasource_vm.VmModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading vm: %s.", vmDataSourceLogIdentifier(&state)))
	vmResp, err := d.uc.client.GetVMDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading vm: %s.", vmDataSourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if vmResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading vm",
			fmt.Sprintf("Received %s for vm: %s. Body: %s", vmResp.Status(), vmDataSourceLogIdentifier(&state), vmResp.Body))
		return
	}

	diags := setVmStateDatasource(ctx, vmResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func setVmStateDatasource(ctx context.Context, vmd *ubicloud_client.Vm, state *datasource_vm.VmModel) diag.Diagnostics {
	state.Id = types.StringValue(vmd.Id)
	state.Name = types.StringValue(vmd.Name)
	state.State = types.StringValue(vmd.State)
	state.Location = types.StringValue(vmd.Location)
	state.Size = types.StringValue(vmd.Size)
	state.UnixUser = types.StringValue(vmd.UnixUser)
	state.StorageSizeGib = types.Int64Value(int64(vmd.StorageSizeGib))
	state.Ip4 = types.StringPointerValue(vmd.Ip4)
	state.Ip6 = types.StringPointerValue(vmd.Ip6)
	state.PrivateIpv4 = types.StringValue(vmd.PrivateIpv4)
	state.PrivateIpv6 = types.StringValue(vmd.PrivateIpv6)
	state.Subnet = types.StringValue(vmd.Subnet)

	firewallsListValue, diags := GetFirewallsState(ctx, vmd.Firewalls)
	if diags.HasError() {
		return diags
	}

	state.Firewalls = firewallsListValue
	return diags
}

func vmDataSourceLogIdentifier(state *datasource_vm.VmModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
