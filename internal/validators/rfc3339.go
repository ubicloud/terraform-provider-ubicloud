// Package validators holds this provider's custom terraform-plugin-framework
// validators, attached to generated schema attributes through the generator-config jq
// patch chain (config/plan_modifiers.jq) so they regenerate into the _gen.go schema
// rather than being hand-patched.
package validators

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

type rfc3339Validator struct{}

func (rfc3339Validator) Description(_ context.Context) string {
	return "value must be an RFC 3339 timestamp, for example 2026-06-24T11:00:00Z"
}

func (v rfc3339Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (rfc3339Validator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ConfigValue.ValueString())); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid RFC 3339 timestamp",
			fmt.Sprintf("value must be an RFC 3339 timestamp, for example 2026-06-24T11:00:00Z: %s", err.Error()),
		)
	}
}

// RFC3339 returns a string validator that rejects values which are not RFC 3339
// timestamps. It runs the exact apply-time check the restore body builder uses
// (time.Parse(time.RFC3339) after trimming surrounding space), so a malformed
// restore_target is rejected at plan with the same verdict it would get at apply,
// closing the gap a coarse regex left open (a regex passes 2026-13-24T.. or ..T24:00:00Z
// that time.Parse rejects). Null and unknown values are skipped per validator convention;
// an unknown (computed) value is still parsed at apply by the body builder.
func RFC3339() validator.String {
	return rfc3339Validator{}
}
