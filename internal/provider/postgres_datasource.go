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

	// A 200 whose body is not JSON parses to a nil JSON200 (a proxy/LB interposition page):
	// fail closed rather than nil-dereference in the state mapper.
	if postgresResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Empty response reading postgres database",
			fmt.Sprintf("the API returned no database body: %s", postgresDataSourceLogIdentifier(&state)),
		)
		return
	}

	diags := setPostgresStateDatasource(ctx, postgresResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func setPostgresStateDatasource(ctx context.Context, postgresd *ubicloud_client.PostgresDatabase, state *datasource_postgres.PostgresModel) diag.Diagnostics {
	state.Id = types.StringValue(postgresd.Id)
	state.Name = types.StringValue(postgresd.Name)
	state.State = types.StringValue(postgresd.State)
	state.Location = types.StringValue(postgresd.Location)
	state.VmSize = types.StringValue(postgresd.VmSize)
	state.StorageSizeGib = types.Int64Value(int64(postgresd.StorageSizeGib))
	state.Primary = types.BoolValue(postgresd.Primary)
	state.HaType = types.StringValue(postgresd.HaType)
	state.Version = types.StringValue(string(postgresd.Version))
	state.ConnectionString = types.StringPointerValue(postgresd.ConnectionString)
	state.EarliestRestoreTime = types.StringPointerValue(postgresd.EarliestRestoreTime)
	state.LatestRestoreTime = types.StringValue(postgresd.LatestRestoreTime)
	state.Flavor = types.StringValue(postgresd.Flavor)
	state.TargetVmSize = types.StringPointerValue(postgresd.TargetVmSize)
	state.TargetStorageSizeGib = int64PointerValue(postgresd.TargetStorageSizeGib)
	state.TargetVersion = types.StringValue(string(postgresd.TargetVersion))
	state.TargetServerCount = types.Int64Value(int64(postgresd.TargetServerCount))
	state.MaintenanceWindowStartAt = int64PointerValue(postgresd.MaintenanceWindowStartAt)
	state.ReadReplica = types.BoolValue(postgresd.ReadReplica)
	state.Parent = types.StringPointerValue(postgresd.Parent)
	state.FallbackActive = types.BoolValue(postgresd.FallbackActive)
	state.CaCertificates = types.StringPointerValue(postgresd.CaCertificates)
	state.CreatedAt = types.StringValue(postgresd.CreatedAt.Format(iso8601Layout))
	state.Hostname = types.StringPointerValue(postgresd.Hostname)
	state.Username = types.StringPointerValue(postgresd.Username)
	state.Password = types.StringPointerValue(postgresd.Password)

	firewallRulesListValue, diags := GetPostgresFirewallRulesState(ctx, postgresd.FirewallRules)
	if diags.HasError() {
		return diags
	}
	state.FirewallRules = firewallRulesListValue

	tagsListValue, tagsDiags := GetPostgresTagsState(ctx, postgresd.Tags)
	diags.Append(tagsDiags...)
	if diags.HasError() {
		return diags
	}
	state.Tags = tagsListValue

	return diags
}

func GetPostgresFirewallRulesState(ctx context.Context, firewallRules []ubicloud_client.PostgresFirewallRule) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics

	firewallRulesValue := datasource_postgres.FirewallRulesValue{}
	firewallRulesValues := make([]datasource_postgres.FirewallRulesValue, 0, len(firewallRules))
	for _, r := range firewallRules {
		fr := datasource_postgres.NewFirewallRulesValueMust(firewallRulesValue.AttributeTypes(ctx), map[string]attr.Value{
			"cidr":        types.StringValue(r.Cidr),
			"description": types.StringPointerValue(r.Description),
			"id":          types.StringValue(r.Id),
			"port":        int64PointerValue(r.Port),
		})
		firewallRulesValues = append(firewallRulesValues, fr)
	}

	firewallRulesListValue, diag := types.ListValueFrom(ctx, firewallRulesValue.Type(ctx), firewallRulesValues)
	diags.Append(diag...)
	if diags.HasError() {
		return basetypes.NewListUnknown(firewallRulesValue.Type(ctx)), diags
	}

	return firewallRulesListValue, diags
}

func GetPostgresTagsState(ctx context.Context, tags []ubicloud_client.PostgresTag) (basetypes.ListValue, diag.Diagnostics) {
	var diags diag.Diagnostics

	tagsValue := datasource_postgres.TagsValue{}
	tagsValues := make([]datasource_postgres.TagsValue, 0, len(tags))
	for _, t := range tags {
		tv := datasource_postgres.NewTagsValueMust(tagsValue.AttributeTypes(ctx), map[string]attr.Value{
			"key":   types.StringValue(t.Key),
			"value": types.StringValue(t.Value),
		})
		tagsValues = append(tagsValues, tv)
	}

	tagsListValue, diag := types.ListValueFrom(ctx, tagsValue.Type(ctx), tagsValues)
	diags.Append(diag...)
	if diags.HasError() {
		return basetypes.NewListUnknown(tagsValue.Type(ctx)), diags
	}

	return tagsListValue, diags
}

func postgresDataSourceLogIdentifier(state *datasource_postgres.PostgresModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
