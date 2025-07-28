# Define paths
PWD := $(PWD)
ENVTEST := $(PWD)/bin/setup-envtest
ENVTEST_CMD := $(ENVTEST)
ENVSUBST := $(PWD)/bin/envsubst
ENVSUBST_CMD := $(ENVSUBST)
CONTROLLER_GEN := $(PWD)/bin/controller-gen
CONTROLLER_GEN_CMD := $(CONTROLLER_GEN)
GO_GETTER := $(PWD)/bin/go-getter
GO_GETTER_CMD := $(GO_GETTER)
GOIMPORTS := $(PWD)/bin/goimports
GOIMPORTS_CMD := $(GOIMPORTS)
STATICCHECK := $(PWD)/bin/staticcheck
STATICCHECK_CMD := $(STATICCHECK)
KUSTOMIZE := $(PWD)/bin/kustomize
KUSTOMIZE_CMD := $(KUSTOMIZE)
KUBEVALIDATE ?= $(PWD)/bin/kubectl-validate
KUBEVALIDATE_CMD ?= $(KUBEVALIDATE)

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.30

# Local Deployment variables
IMAGE_VERSION ?= vdev

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install -modfile=tools/go.mod $(2) ;\
}
endef

DOCKER_CMD ?= docker

.PHONY: test
test: $(ENVTEST)
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST_CMD) --arch=amd64 use $(ENVTEST_K8S_VERSION) -p path)" go test -race -v ./...

.PHONY: lint
lint: $(STATICCHECK) $(GOIMPORTS)
	go mod tidy
	cd tools && go mod tidy
	go fmt ./...
	go vet ./...
	$(STATICCHECK_CMD) ./...
	$(GOIMPORTS_CMD) -local github.com/reddit/progressivedaemonset -l -w .

.PHONY: docker-build
docker-build:
	docker build --target development -t progressive-rollout-controller:$(IMAGE_VERSION) .
    # Optional: For production image use: docker build --target production -t progressive-rollout-controller:$(IMAGE_VERSION) .
	# check if image exists before pulling
	# possible docker/kind bug that requires pulling specific image digest https://github.com/kubernetes-sigs/kind/issues/3795#issuecomment-2652220930
	docker image inspect busybox-arm64:1.37 >/dev/null 2>&1 || docker pull busybox@sha256:fa8dc70514f29471fe118446e3f84040b19791531ec197836a4f43baf13d744b
	docker tag busybox@sha256:fa8dc70514f29471fe118446e3f84040b19791531ec197836a4f43baf13d744b busybox-arm64:1.37

.PHONY: kind
kind: docker-build
	kind create cluster --config examples/manifests/test-cluster-config.yaml
	kind load docker-image progressive-rollout-controller:$(IMAGE_VERSION)
	kind load docker-image busybox-arm64:1.37 --name kind
	kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.18.2/cert-manager.yaml
	kubectl wait --for=condition=available deployment --all -n cert-manager --timeout=120s
	kubectl create namespace progressive-rollout-controller
	make apply-manifests
	@echo "Progressive Rollout Controller is now running in the Kind cluster!"
	@echo "To apply a sample DaemonSet, run: kubectl apply -f examples/manifests/test-daemonset.yaml -n progressive-rollout-controller"
	@echo "To watch the rollout, run: kubectl get pods -n progressive-rollout-controller -w"

.PHONY: apply-manifests
apply-manifests:
	@echo "→ deploying with IMAGE_VERSION=$(IMAGE_VERSION)"
	kustomize build manifests | IMAGE_VERSION=$(IMAGE_VERSION) flux envsubst --strict | kubectl apply --context kind-kind -f - # replace IMAGE_VERSION in deployment for local testing

### Tools
# Helpers to download various build tools

$(CONTROLLER_GEN):
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen)

$(ENVTEST):
	$(call go-get-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest)

$(ENVSUBST):
	curl -L https://github.com/a8m/envsubst/releases/download/v1.2.0/envsubst-`uname -s`-`uname -m` -o $(ENVSUBST)
	chmod +x $(ENVSUBST)

$(GO_GETTER):
	$(call go-get-tool,$(GO_GETTER),github.com/hashicorp/go-getter/cmd/go-getter)

$(GOIMPORTS):
	$(call go-get-tool,$(GOIMPORTS),golang.org/x/tools/cmd/goimports)

$(STATICCHECK):
	$(call go-get-tool,$(STATICCHECK),honnef.co/go/tools/cmd/staticcheck)

$(KUSTOMIZE):
	$(call go-get-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5)

$(WAIT_FOR_CRDS):
	cd ./scripts/wait-for-crds; go build -o ../../bin/wait-for-crds ./main.go

$(CRD_TO_MARKDOWN):
	$(call go-get-tool,$(CRD_TO_MARKDOWN),github.com/clamoriniere/crd-to-markdown)

$(KUBEVALIDATE):
	$(call go-get-tool,$(KUBEVALIDATE),sigs.k8s.io/kubectl-validate)

### Sanity

.PHONY: clean
clean:
	rm -f bin/*