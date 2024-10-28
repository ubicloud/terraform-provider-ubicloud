package main

//go:generate go run github.com/deepmap/oapi-codegen/v2/cmd/oapi-codegen --config=config/go_generator_config.yml config/ubicloud_openapi.yml
//go:generate go run github.com/hashicorp/terraform-plugin-codegen-openapi/cmd/tfplugingen-openapi generate --config config/tf_generator_config.yml --output config/generated/provider_code_spec.json config/ubicloud_openapi.yml
//go:generate sh -c "jq '( .resources[] | select(.name == \"vm\" or .name == \"postgres\" or .name == \"private_subnet\" or .name == \"firewall\") | .schema.attributes[] | select(.name == \"project_id\" or .name == \"location\" or .name == \"name\") ).string.computed_optional_required = \"required\"' config/generated/provider_code_spec.json > config/generated/provider_code_spec_mod.tmp.json"
//go:generate sh -c "jq '( .resources[] | select(.name == \"vm\" or .name == \"private_subnet\") | .schema.attributes[] | select(.name == \"boot_image\" or .name == \"private_subnet_id\" or .name == \"firewall_id\") ).string.computed_optional_required = \"optional\"' config/generated/provider_code_spec_mod.tmp.json > config/generated/provider_code_spec_mod.tmp2.json"
//go:generate sh -c "jq '( .resources[] | select(.name == \"vm\" or .name == \"postgres\") | .schema.attributes[] | select(.name == \"storage_size\") ).int64.computed_optional_required = \"optional\"' config/generated/provider_code_spec_mod.tmp2.json > config/generated/provider_code_spec_mod.tmp3.json"
//go:generate sh -c "jq '( .resources[] | select(.name == \"vm\") | .schema.attributes[] | select(.name == \"enable_ip4\") ).bool.computed_optional_required = \"optional\"' config/generated/provider_code_spec_mod.tmp3.json > config/generated/provider_code_spec_mod.tmp4.json"
//go:generate sh -c "jq '( .resources[] | select(.name == \"load_balancer\") | .schema.attributes[] | select(.name == \"name\") ).string.computed_optional_required = \"required\"' config/generated/provider_code_spec_mod.tmp4.json > config/generated/provider_code_spec_mod.tmp5.json"
//go:generate sh -c "jq '( .resources[] | select(.name == \"load_balancer\") | .schema.attributes[] | select(.name == \"vms\") ).list.computed_optional_required = \"required\"' config/generated/provider_code_spec_mod.tmp5.json > config/generated/provider_code_spec_mod.tmp6.json"
//go:generate sh -c "jq '( .resources[] | select(.name == \"load_balancer\") | .schema.attributes[] | select( .name == \"private_subnet_id\") ).string.computed_optional_required = \"optional\"' config/generated/provider_code_spec_mod.tmp6.json > config/generated/provider_code_spec_mod.json"

//go:generate go run github.com/hashicorp/terraform-plugin-codegen-framework/cmd/tfplugingen-framework generate data-sources --input config/generated/provider_code_spec_mod.json  --output internal/generated
//go:generate go run github.com/hashicorp/terraform-plugin-codegen-framework/cmd/tfplugingen-framework generate resources --input config/generated/provider_code_spec_mod.json  --output internal/generated

//go:generate terraform fmt -recursive ./examples/
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate -provider-name ubicloud

import (
	"context"
	"flag"
	"log"

	"github.com/ubicloud/terraform-provider-ubicloud/internal/provider"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var (
	// these will be set by the goreleaser configuration
	// to appropriate values for the compiled binary.
	version string = "dev"

	// goreleaser can pass other information to the main package, such as the specific commit
	// https://goreleaser.com/cookbooks/using-main.version/
)

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/ubicloud/ubicloud",
		Debug:   debug,
	}

	err := providerserver.Serve(context.Background(), provider.New(version), opts)

	if err != nil {
		log.Fatal(err.Error())
	}
}
