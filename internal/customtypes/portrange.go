package customtypes

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// portRangeRe mirrors the backend's ALLOWED_PORT_RANGE_PATTERN (lib/validation.rb): a
// bare port "X" or a range "X..Y".
var portRangeRe = regexp.MustCompile(`^(\d+)(?:\.\.(\d+))?$`)

// collapsePortRange mirrors the backend canonicalization of a port range. The server
// parses each bound through to_i (lib/validation.rb validate_port_range) and renders the
// stored int4range back via model/firewall_rule.rb display_port_range, collapsing an
// equal-bounds range to a single port. So it normalizes every accepted form through the
// integer value ("022" -> "22", "0022..0023" -> "22..23") and emits "X" for a single
// port or an equal-bounds range, else "X..Y". An unrecognized string (which the server
// would reject) is returned unchanged.
func collapsePortRange(v string) string {
	groups := portRangeRe.FindStringSubmatch(v)
	if groups == nil {
		return v
	}
	start, err := strconv.Atoi(groups[1])
	if err != nil {
		return v
	}
	if groups[2] == "" {
		return strconv.Itoa(start)
	}
	end, err := strconv.Atoi(groups[2])
	if err != nil {
		return v
	}
	if start == end {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + ".." + strconv.Itoa(end)
}

// PortRangeType is a string type whose values compare equal under the backend's port
// range canonicalization, so a user-written equal-bounds range ("22..22") and the single
// port the API stores and returns ("22") are treated as the same value.
type PortRangeType struct {
	basetypes.StringType
}

var (
	_ basetypes.StringTypable                    = PortRangeType{}
	_ basetypes.StringValuableWithSemanticEquals = PortRangeValue{}
)

func (t PortRangeType) Equal(o attr.Type) bool {
	other, ok := o.(PortRangeType)
	if !ok {
		return false
	}
	return t.StringType.Equal(other.StringType)
}

func (t PortRangeType) String() string {
	return "customtypes.PortRangeType"
}

func (t PortRangeType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return PortRangeValue{StringValue: in}, nil
}

func (t PortRangeType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	stringValue, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type of %T", attrValue)
	}
	stringValuable, diags := t.ValueFromString(ctx, stringValue)
	if diags.HasError() {
		return nil, fmt.Errorf("unexpected error converting StringValue to StringValuable: %v", diags)
	}
	return stringValuable, nil
}

func (t PortRangeType) ValueType(_ context.Context) attr.Value {
	return PortRangeValue{}
}

// PortRangeValue is the value type for PortRangeType.
type PortRangeValue struct {
	basetypes.StringValue
}

func (v PortRangeValue) Equal(o attr.Value) bool {
	other, ok := o.(PortRangeValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

func (v PortRangeValue) Type(_ context.Context) attr.Type {
	return PortRangeType{}
}

// StringSemanticEquals treats two port ranges as equal when they collapse to the same
// stored form, so the planned "22..22" and the API's returned "22" do not register as a
// change. This is what lets the framework retain the prior (config) form in state on
// create and avoid both "inconsistent result after apply" and a perpetual diff.
func (v PortRangeValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	newValue, ok := newValuable.(PortRangeValue)
	if !ok {
		diags.AddError(
			"Semantic Equality Check Error",
			fmt.Sprintf("expected value type %T but got %T", v, newValuable),
		)
		return false, diags
	}
	return collapsePortRange(v.ValueString()) == collapsePortRange(newValue.ValueString()), diags
}

// NewPortRangeValue returns a known PortRangeValue for the given string.
func NewPortRangeValue(value string) PortRangeValue {
	return PortRangeValue{StringValue: basetypes.NewStringValue(value)}
}
