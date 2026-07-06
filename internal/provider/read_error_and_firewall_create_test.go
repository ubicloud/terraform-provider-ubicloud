package provider

import (
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/generated/ubicloud_client"
)

// offlineClient wires the real generated client to srv, never a stub.
func offlineClient(t *testing.T, srv *httptest.Server) *UbicloudClient {
	t.Helper()
	client, err := ubicloud_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("NewClientWithResponses: %v", err)
	}
	return &UbicloudClient{client: client}
}

func mkRawFromSchema(objType tftypes.Object, overrides map[string]tftypes.Value) tftypes.Value {
	vals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		if v, ok := overrides[name]; ok {
			vals[name] = v
		} else {
			vals[name] = tftypes.NewValue(typ, nil)
		}
	}
	return tftypes.NewValue(objType, vals)
}
