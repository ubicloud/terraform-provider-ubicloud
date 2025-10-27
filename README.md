# Terraform Provider: Ubicloud

The Ubicloud provider enables Terraform to manage resources supported by [Ubicloud](https://www.ubicloud.com/).

## Documentation

Official documentation on how to use this provider can be found on the 
[Terraform Registry](https://registry.terraform.io/providers/ubicloud/ubicloud/latest/docs).


The remainder of this document will focus on the development aspects of the provider.

## Requirements

* [Terraform](https://www.terraform.io/downloads)
* [Go](https://go.dev/doc/install) (1.22)
* [GNU Make](https://www.gnu.org/software/make/)
* [jq](https://jqlang.github.io/jq/)

## Development

### Building

1. `git clone` this repository and `cd` into its directory
2. `make` will trigger the Golang build

The provided [GNUmakefile](./GNUmakefile) defines additional commands generally useful during development.

#### Code generation

This project uses [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen/) to generate a Go client to interact with Ubicloud based on an [OpenAPI specification of the Ubicloud API](./config/ubicloud_openapi.yml).

It also uses [OpenAPI Provider Spec Generator](https://github.com/hashicorp/terraform-plugin-codegen-openapi) together with [Terraform Plugin Framework Code Generator](github.com/hashicorp/terraform-plugin-codegen-framework) to generate parts of the Ubicloud provider itself, based on the same [OpenAPI spec](./config/ubicloud_openapi.yml).

#### Documentation generation

This provider uses [terraform-plugin-docs](https://github.com/hashicorp/terraform-plugin-docs/)
to generate documentation as part of the build, which is stored in the `docs/` directory.


#### Using a locally built Ubicloud provider

First, use `make install` to place a fresh development build of the provider in your
[`${GOBIN}`](https://pkg.go.dev/cmd/go#hdr-Compile_and_install_packages_and_dependencies)
(defaults to `${GOPATH}/bin` or `${HOME}/go/bin` if `${GOPATH}` is not set). Repeat
this every time you make changes to the provider locally.

Then, in your `${HOME}/.terraformrc` (Unix) / `%APPDATA%\terraform.rc` (Windows), add a `provider_installation` that contains
the following `dev_overrides`:

```hcl
provider_installation {
  dev_overrides {
    "ubicloud/ubicloud" = "${GOBIN}" //< replace `${GOBIN}` with the actual path on your system
  }

  direct {}
}
```

For more details check out the Terraform documentation on 
[development overrides for provider-developers](https://www.terraform.io/cli/config/config-file#development-overrides-for-provider-developers).

### Testing

In order to test the provider, you can run

* `make testacc` to run provider acceptance tests

**Important:** Acceptance tests (`testacc`) will actually spawn
`terraform` and the provider, and create real resources on Ubicloud. Read more about acceptance tests on the
[official Terraform page](https://www.terraform.io/plugin/sdkv2/testing/acceptance-tests).

Acceptance tests require the definition of these environment variables:
* `UBICLOUD_ACC_TEST_PROJECT` ID of an existing project. Resources will be created in that project.
* `UBICLOUD_ACC_TEST_LOCATION` Location name, e.g. 'eu-central-h1'. Resources will be created in that location.
* `UBICLOUD_ACC_TEST_FIREWALL` ID of an existing firewall. Some of the resources will be attached to that firewall.
* `UBICLOUD_ACC_TEST_PRIVATE_SUBNET` ID of an existing private subnet. Needs to be in the same location as defined above. Some of the resources will be created in that subnet. 

## Releasing

The release process is automated via GitHub Actions, and it's defined in the Workflow
[release.yml](./.github/workflows/release.yml).

Each release is cut by pushing a [semantically versioned](https://semver.org/) tag to the default branch.

## License

[Mozilla Public License v2.0](./LICENSE)