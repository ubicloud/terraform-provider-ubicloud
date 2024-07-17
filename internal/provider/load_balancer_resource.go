package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_load_balancer"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &loadBalancerResource{}
	_ resource.ResourceWithConfigure   = &loadBalancerResource{}
	_ resource.ResourceWithImportState = &loadBalancerResource{}
)

func NewLoadBalancerResource() resource.Resource {
	return &loadBalancerResource{}
}

type loadBalancerResource struct {
	uc *UbicloudClient
}

func (r *loadBalancerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *loadBalancerResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (r *loadBalancerResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_load_balancer.LoadBalancerResourceSchema(ctx)
	resp.Schema.Description = "Provides a Ubicloud LoadBalancer resource. This can be used to create and delete load balancers."

}

func (r *loadBalancerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_load_balancer.LoadBalancerModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := ubicloud_client.CreateLoadBalancerJSONRequestBody{}
	body.PrivateSubnetId = state.PrivateSubnetId.ValueString()
	body.SrcPort = int(state.SrcPort.ValueInt64())
	body.DstPort = int(state.DstPort.ValueInt64())
	body.Algorithm = ubicloud_client.CreateLoadBalancerJSONBodyAlgorithm(state.Algorithm.ValueString())
	body.HealthCheckEndpoint = state.HealthCheckEndpoint.ValueString()

	tflog.Debug(ctx, fmt.Sprintf("Creating load balancer: %s", loadBalancerResourceLogIdentifier(&state)))
	LoadBalancerResp, err := r.uc.client.CreateLoadBalancerWithResponse(ctx, state.ProjectId.ValueString(), state.Name.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error creating load balancer: %s", loadBalancerResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if LoadBalancerResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code creating LoadBalancer",
			fmt.Sprintf("Received %s for load balancer: %s. Details: %s", LoadBalancerResp.Status(), loadBalancerResourceLogIdentifier(&state), LoadBalancerResp.Body))
		return
	}

	diags := setLoadBalancerStateResource(ctx, LoadBalancerResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *loadBalancerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_load_balancer.LoadBalancerModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Reading load balancer: %s", loadBalancerResourceLogIdentifier(&state)))
	LoadBalancerResp, err := r.uc.client.GetLoadBalancerDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading load balancer: %s", loadBalancerResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if LoadBalancerResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading load balancer",
			fmt.Sprintf("Received %s for load balancer %s. Details: %s", LoadBalancerResp.Status(), loadBalancerResourceLogIdentifier(&state), LoadBalancerResp.Body))
		return
	}

	diags := setLoadBalancerStateResource(ctx, LoadBalancerResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *loadBalancerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state resource_load_balancer.LoadBalancerModel
	var plan resource_load_balancer.LoadBalancerModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Name.IsUnknown() && plan.Name.ValueString() != state.Name.ValueString() {
		resp.Diagnostics.AddError("Can't mutate name of existing machine", "Can't switch name "+state.Name.ValueString()+" to "+plan.Name.ValueString())
	}

	body := ubicloud_client.UpdateLoadBalancerJSONRequestBody{}
	body.SrcPort = int(plan.SrcPort.ValueInt64())
	body.DstPort = int(plan.DstPort.ValueInt64())
	body.Algorithm = ubicloud_client.UpdateLoadBalancerJSONBodyAlgorithm(plan.Algorithm.ValueString())
	body.HealthCheckEndpoint = plan.HealthCheckEndpoint.ValueString()

	listValues, _ := plan.Vms.ToListValue(ctx)
	body.Vms = make([]string, len(listValues.Elements()))

	for i, v := range listValues.Elements() {
		body.Vms[i] = v.String()
	}

	tflog.Debug(ctx, fmt.Sprintf("Updating load balancer: %s", loadBalancerResourceLogIdentifier(&state)))
	LoadBalancerResp, err := r.uc.client.UpdateLoadBalancerWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error updating load balancer: %s", loadBalancerResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if LoadBalancerResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code updating LoadBalancer",
			fmt.Sprintf("Received %s for load balancer: %s. Details: %s", LoadBalancerResp.Status(), loadBalancerResourceLogIdentifier(&state), LoadBalancerResp.Body))
		return
	}

	diags := setLoadBalancerStateResource(ctx, LoadBalancerResp.JSON200, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *loadBalancerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resource_load_balancer.LoadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, fmt.Sprintf("Deleting load balancer: %s", loadBalancerResourceLogIdentifier(&state)))
	LoadBalancerResp, err := r.uc.client.DeleteLoadBalancerWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error deleting load balancer: %s", loadBalancerResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if LoadBalancerResp.StatusCode() != http.StatusNoContent && LoadBalancerResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code deleting load balancer",
			fmt.Sprintf("Received %s for load balancer: %s. Details: %s", LoadBalancerResp.Status(), loadBalancerResourceLogIdentifier(&state), LoadBalancerResp.Body))
		return
	}
}

func (r *loadBalancerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	idParts := strings.Split(req.ID, ",")

	if len(idParts) != 3 || idParts[0] == "" || idParts[1] == "" || idParts[2] == "" {
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			fmt.Sprintf("Expected import identifier with format: project_id,location,name. Got: %q", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), idParts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("location"), idParts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), idParts[2])...)
}

func setLoadBalancerStateResource(ctx context.Context, lb *ubicloud_client.LoadBalancer, state *resource_load_balancer.LoadBalancerModel) diag.Diagnostics {
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

func loadBalancerResourceLogIdentifier(state *resource_load_balancer.LoadBalancerModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, name=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.Name.ValueString())
}
