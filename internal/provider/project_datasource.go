package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/datasource_project"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &projectDataSource{}
	_ datasource.DataSourceWithConfigure = &projectDataSource{}
)

func NewProjectDataSource() datasource.DataSource {
	return &projectDataSource{}
}

type projectDataSource struct {
	uc *UbicloudClient
}

func (d *projectDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *projectDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = datasource_project.ProjectDataSourceSchema(ctx)
	resp.Schema.Description = "Get information about a Ubicloud project."
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state datasource_project.ProjectModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading project: project_id=%s", state.Id.ValueString()))
	projectResp, err := d.uc.client.GetProjectWithResponse(ctx, state.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading project: project_id=%s", state.Id.ValueString()),
			err.Error(),
		)
		return
	}

	if projectResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading project",
			fmt.Sprintf("Received %s for project: project_id=%s'. Details: %s", projectResp.Status(), state.Id.ValueString(), projectResp.Body))
		return
	}

	// A non-JSON 200 (proxy interposition) leaves JSON200 nil; fail closed rather than nil-deref.
	if projectResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Empty response reading project",
			fmt.Sprintf("the API returned no project body: project_id=%s", state.Id.ValueString()),
		)
		return
	}

	state.Id = types.StringValue(projectResp.JSON200.Id)
	state.Name = types.StringValue(projectResp.JSON200.Name)
	state.Discount = types.Int64Value(int64(projectResp.JSON200.Discount))
	state.Credit = types.Float64Value(float64(projectResp.JSON200.Credit))

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
