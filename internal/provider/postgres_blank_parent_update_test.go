package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Clearing parent (an explicit blank) would promote a read replica, whose endpoint the
// provider does not wire; without this guard the blank only fails later in Create's apply backstop.
func TestModifyPlanUpdateRejectsBlankParent(t *testing.T) {
	ctx := t.Context()
	for _, blank := range []struct{ name, val string }{
		{"empty", ""},
		{"whitespace", "   "},
	} {
		t.Run(blank.name, func(t *testing.T) {
			resp := driveModifyPlan(t, ctx,
				map[string]tftypes.Value{
					"read_replica": boolRaw(true),
					"parent":       strRaw("tf-acc-src-a"),
				},
				map[string]tftypes.Value{
					"parent": strRaw(blank.val),
				},
			)
			errs := resp.Diagnostics.Errors()
			if len(errs) != 1 || errs[0].Summary() != "Blank parent" {
				t.Fatalf("want exactly one \"Blank parent\" error, got %+v", resp.Diagnostics)
			}
			if !strings.Contains(errs[0].Detail(), "keep the current database") {
				t.Fatalf("blank-parent update error should point to keeping the database, got %q", errs[0].Detail())
			}
		})
	}
}

func TestModifyPlanUpdateBlankParentBeatsInheritedArm(t *testing.T) {
	ctx := t.Context()
	resp := driveModifyPlan(t, ctx,
		map[string]tftypes.Value{
			"read_replica": boolRaw(true),
			"parent":       strRaw("tf-acc-src-a"),
		},
		map[string]tftypes.Value{
			"parent": strRaw(""),
			"size":   strRaw("m8gd.large"),
		},
	)
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 || errs[0].Summary() != "Blank parent" {
		t.Fatalf("clearing parent must draw the blank-parent error alone, got %+v", resp.Diagnostics)
	}
	if diagHasDetailContaining(resp.Diagnostics, "cannot be changed on a read replica") {
		t.Fatalf("clearing parent must not draw the misleading read-replica message: %+v", resp.Diagnostics)
	}
}
