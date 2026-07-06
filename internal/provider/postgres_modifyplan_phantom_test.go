package provider

import (
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func boolRaw(b bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, b) }
