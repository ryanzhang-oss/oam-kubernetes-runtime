package v1alpha2

import (
	"github.com/crossplane/oam-kubernetes-runtime/pkg/webhook/v1alpha2/applicationconfiguration"
	"github.com/crossplane/oam-kubernetes-runtime/pkg/webhook/v1alpha2/component"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// Add will be called in main and register all validation handlers
func Add(mgr manager.Manager) error {
	if err := applicationconfiguration.RegisterValidatingHandler(mgr); err != nil {
		return err
	}
	applicationconfiguration.RegisterMutatingHandler(mgr)
	component.RegisterMutatingHandler(mgr)
	component.RegisterValidatingHandler(mgr)
	return nil
}
