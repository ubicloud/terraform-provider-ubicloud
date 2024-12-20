default: build

.PHONY: build install testacc

build:
	go generate && go build -v ./...

install: build
	go install -v ./...

testacc:
	TF_ACC=1 go test ./internal/provider/ -count=1 -v -cover -timeout 10m -skip TestAccProjectResource
