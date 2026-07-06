package provider

import (
	"testing"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

// NewFirewallRulesValueMust panics on a partial attribute map, which the create->read
// path hits when the backend seeds default rules; nil input must yield a known empty list.
func TestGetPostgresFirewallRulesState(t *testing.T) {
	ctx := t.Context()
	port := 5432
	desc := "Allow all"
	rules := []ubicloud_client.PostgresFirewallRule{
		{Id: "fr0000000000000000000000aa", Cidr: "0.0.0.0/0", Description: &desc, Port: &port},
		{Id: "fr0000000000000000000000bb", Cidr: "::/0"}, // optional description/port nil -> null
	}

	listValue, diags := GetPostgresFirewallRulesState(ctx, rules)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if listValue.IsNull() || listValue.IsUnknown() {
		t.Fatal("expected a known list value")
	}
	if got := len(listValue.Elements()); got != 2 {
		t.Fatalf("expected 2 firewall rule elements, got %d", got)
	}

	emptyValue, diags := GetPostgresFirewallRulesState(ctx, nil)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics for nil input: %v", diags)
	}
	if emptyValue.IsNull() || emptyValue.IsUnknown() {
		t.Fatal("expected a known empty list value for nil input")
	}
	if got := len(emptyValue.Elements()); got != 0 {
		t.Fatalf("expected 0 elements for nil input, got %d", got)
	}
}
