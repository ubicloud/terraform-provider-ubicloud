package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

func int64PointerValue(source *int) basetypes.Int64Value {
	if source == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*source))
}
