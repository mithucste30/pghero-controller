.PHONY: help build test docker-build docker-push install uninstall deploy undeploy

# Image URL to use for building/pushing image targets
IMG ?= ghcr.io/mithucste30/pghero-controller:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

fmt: ## Run go fmt against code
	go fmt ./...

vet: ## Run go vet against code
	go vet ./...

test: fmt vet ## Run tests
	go test ./... -coverprofile cover.out

build: fmt vet ## Build controller binary
	go build -o bin/manager cmd/controller/main.go

run: fmt vet ## Run controller from your host
	go run ./cmd/controller/main.go

##@ Docker

docker-build: ## Build docker image
	docker build -t ${IMG} .

docker-push: ## Push docker image
	docker push ${IMG}

##@ Deployment

install-crd: ## Install CRDs into the cluster
	kubectl apply -f config/crd/

uninstall-crd: ## Uninstall CRDs from the cluster
	kubectl delete -f config/crd/

install: ## Install controller using Helm
	helm install pghero-controller ./helm/pghero-controller \
		--namespace pghero-system \
		--create-namespace \
		--set image.repository=$(shell echo ${IMG} | cut -d: -f1) \
		--set image.tag=$(shell echo ${IMG} | cut -d: -f2)

uninstall: ## Uninstall controller using Helm
	helm uninstall pghero-controller --namespace pghero-system

upgrade: ## Upgrade controller using Helm
	helm upgrade pghero-controller ./helm/pghero-controller \
		--namespace pghero-system \
		--set image.repository=$(shell echo ${IMG} | cut -d: -f1) \
		--set image.tag=$(shell echo ${IMG} | cut -d: -f2)

##@ Helm

helm-lint: ## Lint Helm chart
	helm lint ./helm/pghero-controller

helm-template: ## Render Helm templates
	helm template pghero-controller ./helm/pghero-controller

helm-package: ## Package Helm chart
	helm package ./helm/pghero-controller -d dist/

##@ Build Dependencies

deps: ## Download Go dependencies
	go mod download

tidy: ## Tidy Go dependencies
	go mod tidy

verify: ## Verify Go dependencies
	go mod verify
