package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_postgres"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &postgresDataSource{}
	_ datasource.DataSourceWithConfigure = &postgresDataSource{}
)

func NewPostgresDataSource() datasource.DataSource {
	return &postgresDataSource{}
}

type postgresDataSource struct {
	uc *UbicloudClient
}

func (d *postgresDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *postgresDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_postgres"
}

func (d *postgresDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = datasource_postgres.PostgresDataSourceSchema(ctx)
	resp.Schema.Description = "Get information about a Ubicloud PostgreSQL database."
}

func (d *postgresDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state datasource_postgres.PostgresModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading postgres database: %s.", postgresDataSourceLogIdentifier(&state)))
	postgresResp, err := d.uc.client.GetPostgresDatabaseDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading postgres database: %s.", postgresDataSourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if postgresResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading postgres database",
			fmt.Sprintf("Received %s reading postgres database: %s. Details: %s", postgresResp.Status(), postgresDataSourceLogIdentifier(&state), postgresResp.Body))
		return
	}

	diags := setPostgresStateDatasource(ctx, postgresResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func setPostgresStateDatasource(ctx context.Context, postgresd *ubicloud_client.PostgresDetailed, state *datasource_postgres.PostgresModel) diag.Diagnostics {
	assignStr(postgresd.Id, &state.Id)
	assignStr(postgresd.Name, &state.Name)
	assignStr(postgresd.State, &state.State)
	assignStr(postgresd.Location, &state.Location)
	assignStr(postgresd.VmSize, &state.VmSize)
	assignStr(postgresd.VmSize, &state.VmSize)
	assignInt(postgresd.StorageSizeGib, &state.StorageSizeGib)
	assignBool(postgresd.Primary, &state.Primary)
	assignStr(postgresd.HaType, &state.HaType)
	assignStr(postgresd.ConnectionString, &state.ConnectionString)
	assignStr(postgresd.EarliestRestoreTime, &state.EarliestRestoreTime)
	assignStr(postgresd.LatestRestoreTime, &state.LatestRestoreTime)

	firewallRulesListValue, diags := GetPostgresFirewallRulesState(ctx, postgresd.FirewallRules)
	if diags.HasError() {
		return diags
	}

	state.FirewallRules = firewallRulesListValue
	return diags
}

func GetPostgresFirewallRulesState(ctx context.Context, firewallRules *[]ubicloud_client.PostgresFirewallRule) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics

	firewallRulesValue := datasource_postgres.FirewallRulesValue{}
	var firewallRulesValues []datasource_postgres.FirewallRulesValue
	if firewallRules != nil && len(*firewallRules) > 0 {
		for _, r := range *firewallRules {
			fr := datasource_postgres.NewFirewallRulesValueMust(firewallRulesValue.AttributeTypes(ctx), map[string]attr.Value{
				"id":   types.StringPointerValue(r.Id),
				"cidr": types.StringPointerValue(r.Cidr),
			})
			firewallRulesValues = append(firewallRulesValues, fr)
		}
	} else {
		firewallRulesValues = []datasource_postgres.FirewallRulesValue{}
	}

	firewallRulesListValue, diag := types.ListValueFrom(ctx, firewallRulesValue.Type(ctx), firewallRulesValues)
	diags.Append(diag...)
	if diags.HasError() {
		return basetypes.NewListUnknown(firewallRulesValue.Type(ctx)), diags
	}

	return firewallRulesListValue, diags
}

func postgresDataSourceLogIdentifier(state *datasource_postgres.PostgresModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
