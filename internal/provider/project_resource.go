package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_project"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &projectResource{}
	_ resource.ResourceWithConfigure   = &projectResource{}
	_ resource.ResourceWithImportState = &projectResource{}
)

func NewProjectResource() resource.Resource {
	return &projectResource{}
}

type projectResource struct {
	uc *UbicloudClient
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *projectResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_project.ProjectResourceSchema(ctx)
	resp.Schema.Description = "Provides a Ubicloud Project resource. This can be used to create and delete projects."

}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_project.ProjectModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := ubicloud_client.CreateProjectJSONRequestBody{Name: state.Name.ValueString()}

	tflog.Debug(ctx, "Creating project")
	projectResp, err := r.uc.client.CreateProjectWithResponse(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating project",
			err.Error(),
		)
		return
	}

	if projectResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code creating project",
			fmt.Sprintf("Received %s creating project. Details: %s", projectResp.Status(), projectResp.Body))
		return
	}

	assignStr(projectResp.JSON200.Id, &state.Id)
	assignStr(projectResp.JSON200.Name, &state.Name)
	assignInt(projectResp.JSON200.Discount, &state.Discount)
	assignFloat(projectResp.JSON200.Credit, &state.Credit)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_project.ProjectModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading project: project_id=%s", state.Id.ValueString()))
	projectResp, err := r.uc.client.GetProjectWithResponse(ctx, state.Id.ValueString())
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
			fmt.Sprintf("Received %s for project: project_id=%s. Details: %s", projectResp.Status(), state.Id.ValueString(), projectResp.Body))
		return
	}

	assignStr(projectResp.JSON200.Id, &state.Id)
	assignStr(projectResp.JSON200.Name, &state.Name)
	assignInt(projectResp.JSON200.Discount, &state.Discount)
	assignFloat(projectResp.JSON200.Credit, &state.Credit)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state resource_project.ProjectModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddError(
		"Update of project is not supported",
		fmt.Sprintf("Cannot update project: project_id=%s", state.Id.ValueString()))
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resource_project.ProjectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Deleting project: project_id=%s", state.Id.ValueString()))
	projectResp, err := r.uc.client.DeleteProjectWithResponse(ctx, state.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error deleting project: project_id=%s", state.Id.ValueString()),
			err.Error(),
		)
		return
	}

	if projectResp.StatusCode() != http.StatusNoContent && projectResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code deleting project",
			fmt.Sprintf("Received %s for project: project_id=%s. Details: %s", projectResp.Status(), state.Id.ValueString(), projectResp.Body))
		return
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
