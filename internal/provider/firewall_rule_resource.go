package provider

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/customtypes"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/resource_firewall_rule"
	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// firewallUbidRe matches a firewall UBID, mirroring the id pattern in the OpenAPI
// Firewall schema. A firewall_reference that matches is an id; otherwise it is a name.
var firewallUbidRe = regexp.MustCompile(`^fw[0-9a-hj-km-np-tv-z]{24}$`)

var (
	_ resource.Resource                = &firewallRuleResource{}
	_ resource.ResourceWithConfigure   = &firewallRuleResource{}
	_ resource.ResourceWithImportState = &firewallRuleResource{}
)

func NewFirewallRuleResource() resource.Resource {
	return &firewallRuleResource{}
}

type firewallRuleResource struct {
	uc *UbicloudClient
}

func (r *firewallRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *firewallRuleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_rule"
}

func (r *firewallRuleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = resource_firewall_rule.FirewallRuleResourceSchema(ctx)
	resp.Schema.Description = "Provides a Ubicloud FirewallRule resource. This can be used to create and delete firewall rules."
}

func (r *firewallRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var state resource_firewall_rule.FirewallRuleModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cidr := state.Cidr.ValueString()
	if firewallRuleCidrNotLiteral(cidr) {
		resp.Diagnostics.AddError(
			"Unsupported cidr for ubicloud_firewall_rule",
			fmt.Sprintf("cidr %q is not a literal IPv4 or IPv6 CIDR. The API treats a non-literal value as a private subnet reference, which expands into one rule per subnet address family or is rejected; a single ubicloud_firewall_rule resource can represent neither. Specify an explicit IPv4 or IPv6 CIDR instead.", cidr),
		)
		return
	}

	body := ubicloud_client.CreateLocationFirewallRuleJSONRequestBody{
		Cidr: cidr,
	}
	if state.PortRange.ValueString() != "" {
		body.PortRange = state.PortRange.ValueStringPointer()
	}
	if !state.Description.IsNull() && !state.Description.IsUnknown() {
		body.Description = state.Description.ValueStringPointer()
	}
	if !state.Protocol.IsNull() && !state.Protocol.IsUnknown() {
		body.Protocol = state.Protocol.ValueStringPointer()
	}

	firewallRef := firewallReference(state.FirewallId.ValueString(), state.FirewallName.ValueString())
	tflog.Debug(ctx, fmt.Sprintf("Creating firewall rule: project_id=%s, location=%s, firewall=%s", state.ProjectId.ValueString(), state.Location.ValueString(), firewallRef))
	firewallRuleResp, err := r.uc.client.CreateLocationFirewallRuleWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), firewallRef, body)
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error creating firewall rule: project_id=%s, location=%s, firewall=%s", state.ProjectId.ValueString(), state.Location.ValueString(), firewallRef),
			err.Error(),
		)
		return
	}

	if firewallRuleResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code creating firewall rule",
			fmt.Sprintf("Received %s creating new firewall rule: project_id=%s, location=%s, firewall=%s. Details: %s", firewallRuleResp.Status(), state.ProjectId.ValueString(), state.Location.ValueString(), firewallRef, firewallRuleResp.Body))
		return
	}

	// The 200 schema is anyOf {FirewallRule, []FirewallRule}; the array form is the
	// fan-out a private subnet reference would trigger. We reject such cidrs above, so
	// the result must be a single rule. Accept the single-object and single-element
	// array encodings, but refuse a multi-rule result rather than tracking a partial set.
	firewallRule, err := firewallRuleResp.JSON200.AsFirewallRule()
	if err != nil {
		rules, arrErr := firewallRuleResp.JSON200.AsFirewallRuleOrRules1()
		if arrErr != nil || len(rules) == 0 {
			resp.Diagnostics.AddError(
				"Error parsing firewall rule response",
				err.Error(),
			)
			return
		}
		if len(rules) > 1 {
			resp.Diagnostics.AddError(
				"Firewall rule input expanded to multiple rules",
				fmt.Sprintf("The create returned %d firewall rules; a single ubicloud_firewall_rule resource cannot track more than one. These rules now exist server-side and must be removed manually.", len(rules)),
			)
			return
		}
		firewallRule = rules[0]
	}

	setFirewallRuleResourceState(&state, firewallRule)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *firewallRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state resource_firewall_rule.FirewallRuleModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	firewallRef := firewallReference(state.FirewallId.ValueString(), state.FirewallName.ValueString())
	tflog.Debug(ctx, fmt.Sprintf("Reading firewall rule: %s", firewallRuleResourceLogIdentifier(&state)))
	firewallRuleResp, err := r.uc.client.GetLocationFirewallFirewallRuleDetailsWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), firewallRef, state.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error reading firewall rule: %s", firewallRuleResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallRuleResp.StatusCode() != http.StatusOK {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code reading firewall rule",
			fmt.Sprintf("Received %s for firewall rule: %s. Details: %s", firewallRuleResp.Status(), firewallRuleResourceLogIdentifier(&state), firewallRuleResp.Body))
		return
	}

	setFirewallRuleResourceState(&state, *firewallRuleResp.JSON200)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *firewallRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state resource_firewall_rule.FirewallRuleModel

	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddError(
		"Update of firewall rule is not supported",
		fmt.Sprintf("Cannot update firewall rule: %s", firewallRuleResourceLogIdentifier(&state)))
}

func (r *firewallRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state resource_firewall_rule.FirewallRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	firewallRef := firewallReference(state.FirewallId.ValueString(), state.FirewallName.ValueString())
	tflog.Debug(ctx, fmt.Sprintf("Deleting firewall rule: %s", firewallRuleResourceLogIdentifier(&state)))
	firewallRuleResp, err := r.uc.client.DeleteLocationFirewallFirewallRuleWithResponse(ctx, state.ProjectId.ValueString(), state.Location.ValueString(), firewallRef, state.Id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			fmt.Sprintf("Error deleting firewall rule: %s", firewallRuleResourceLogIdentifier(&state)),
			err.Error(),
		)
		return
	}

	if firewallRuleResp.StatusCode() != http.StatusNoContent && firewallRuleResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Unexpected HTTP status code deleting firewallRule",
			fmt.Sprintf("Received %s deleting firewall rule: %s. Details: %s", firewallRuleResp.Status(), fmt.Sprintf("Deleting firewall rule: %s", firewallRuleResourceLogIdentifier(&state)), firewallRuleResp.Body))
		return
	}
}

func (r *firewallRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	idParts := strings.Split(req.ID, ",")

	if len(idParts) != 4 || idParts[0] == "" || idParts[1] == "" || idParts[2] == "" || idParts[3] == "" {
		resp.Diagnostics.AddError(
			"Unexpected Import Identifier",
			fmt.Sprintf("Expected import identifier with format: project_id,location,firewall_reference,id. Got: %q", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), idParts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("location"), idParts[1])...)
	// idParts[2] is the firewall reference: route a UBID to firewall_id and a name to
	// firewall_name so the imported state matches how the parent firewall was referenced.
	if firewallReferenceIsId(idParts[2]) {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("firewall_id"), idParts[2])...)
	} else {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("firewall_name"), idParts[2])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), idParts[3])...)
}

func firewallRuleResourceLogIdentifier(state *resource_firewall_rule.FirewallRuleModel) string {
	return fmt.Sprintf("project_id=%s, location=%s, firewall_name=%s, rule_id=%s", state.ProjectId.ValueString(), state.Location.ValueString(), state.FirewallName.ValueString(), state.Id.ValueString())
}

// firewallRuleCidrNotLiteral reports whether cidr is something other than a literal IPv4
// or IPv6 cidr. The backend (helpers/firewall.rb firewall_rule_params) routes such a value
// down the private-subnet-reference branch, which expands a resolvable subnet into one
// rule per address family (fan-out) and otherwise rejects it; a single
// ubicloud_firewall_rule resource can represent neither, so the provider refuses these up
// front rather than tracking a partial result.
func firewallRuleCidrNotLiteral(cidr string) bool {
	return !strings.Contains(cidr, ".") && !strings.Contains(cidr, ":")
}

// firewallReferenceIsId reports whether a firewall reference is a firewall UBID rather
// than a name.
func firewallReferenceIsId(reference string) bool {
	return firewallUbidRe.MatchString(reference)
}

// firewallReference selects the firewall_reference path parameter. The endpoint accepts a
// firewall ID or name, so firewall_id is preferred when set and firewall_name is the
// fallback.
func firewallReference(firewallId, firewallName string) string {
	if firewallId != "" {
		return firewallId
	}
	return firewallName
}

// setFirewallRuleResourceState maps a FirewallRule response onto the resource model. The
// firewall reference attributes (firewall_id, firewall_name) are Optional-only write-only
// inputs the API never echoes, so the model keeps the configured value (a known string or
// null) and the mapper leaves them untouched.
func setFirewallRuleResourceState(state *resource_firewall_rule.FirewallRuleModel, firewallRule ubicloud_client.FirewallRule) {
	state.Id = types.StringValue(firewallRule.Id)
	state.Cidr = types.StringValue(firewallRule.Cidr)
	state.PortRange = customtypes.NewPortRangeValue(firewallRule.PortRange)
	state.Description = types.StringValue(firewallRule.Description)
	state.Protocol = types.StringValue(string(firewallRule.Protocol))
}
