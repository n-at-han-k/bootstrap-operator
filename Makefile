IMG ?= controller:latest
CONTAINER_TOOL ?= docker
CONTROLLER_GEN ?= $(shell which controller-gen)

#---------------------------------------------------------------------------------
.PHONY: generate
generate: ## Generate DeepCopy methods
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."


# --------------------------------------------------------------------------------
.PHONY: manifests
manifests: ## Generate CRD and RBAC manifests
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd paths="./..." output:crd:artifacts:config=config/crd/bases


#---------------------------------------------------------------------------------
.PHONY: fmt
fmt: ## Format code
	go fmt ./...


#---------------------------------------------------------------------------------
.PHONY: build
build: generate manifests fmt ## Build binary
	go build -o bin/manager cmd/main.go


#---------------------------------------------------------------------------------
.PHONY: run
run: generate manifests fmt ## Run locally
	go run ./cmd/main.go


#---------------------------------------------------------------------------------
.PHONY: docker-build
docker-build: ## Build docker image
	$(CONTAINER_TOOL) build -t $(IMG) .


#---------------------------------------------------------------------------------
.PHONY: docker-push
docker-push: ## Push docker image
	$(CONTAINER_TOOL) push $(IMG)
