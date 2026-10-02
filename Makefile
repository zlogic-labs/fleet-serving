# fleet-serving build entry points.
#
# Pure Go: no cgo, no C toolchain. The controller binary is statically linked
# and cross-compiles from any host, same as the gateway it reports to.

CGO_ENABLED ?= 0
GOBIN       := $(CURDIR)/bin
CMD        := fleet-operator
PKGS       := ./...

.DEFAULT_GOAL := build

# The CRD manifests are generated, not hand-written, and a cluster running a
# stale CRD fails in a way that looks like a controller bug. The generator is
# pinned because a newer one changes the emitted schema.
CONTROLLER_GEN_VERSION := v0.17.3
CONTROLLER_GEN = go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

# linux/amd64 covers NVIDIA and the mainstream domestic GPU cards; linux/arm64
# covers Ascend 910B on Kunpeng; darwin/arm64 is for local development.
RELEASE_TARGETS := linux/amd64 linux/arm64 darwin/arm64

.PHONY: manifests
manifests: ## Regenerate the CRD manifests and deepcopy functions
	$(CONTROLLER_GEN) object paths=./api/...
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=config/crd/bases
	go mod tidy

.PHONY: build
build: manifests ## Compile the controller for the host platform into ./bin
	@mkdir -p $(GOBIN)
	CGO_ENABLED=$(CGO_ENABLED) go build -o $(GOBIN)/$(CMD) ./cmd/manager

.PHONY: build-release
build-release: ## Cross-compile for every supported platform
	@for target in $(RELEASE_TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "  build $(CMD) for $$target"; \
		CGO_ENABLED=$(CGO_ENABLED) GOOS=$$os GOARCH=$$arch \
			go build -o $(GOBIN)/$$os-$$arch/$(CMD) ./cmd/manager || exit 1; \
	done

.PHONY: test
test: ## Run the unit tests
	CGO_ENABLED=$(CGO_ENABLED) go test $(PKGS)

.PHONY: vet
vet: ## Run go vet
	CGO_ENABLED=$(CGO_ENABLED) go vet $(PKGS)

.PHONY: fmt
fmt: ## Format every package
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if anything is unformatted
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

.PHONY: tidy
tidy: ## Tidy the module
	go mod tidy

.PHONY: check
check: fmt-check vet test ## Everything CI runs

.PHONY: clean
clean:
	rm -rf $(GOBIN) coverage.out

.PHONY: help
help: ## List targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
