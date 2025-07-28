//go:build tools

package tools

import (
	_ "github.com/clamoriniere/crd-to-markdown"
	_ "github.com/hashicorp/go-getter/cmd/go-getter"
	_ "golang.org/x/tools/cmd/goimports"
	_ "honnef.co/go/tools/cmd/staticcheck"
	_ "sigs.k8s.io/controller-runtime/tools/setup-envtest"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
	_ "sigs.k8s.io/kustomize/kustomize/v5"
)
