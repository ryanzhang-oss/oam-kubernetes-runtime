/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package applicationconfiguration

import (
	"context"

	runtimev1alpha1 "github.com/crossplane/crossplane-runtime/apis/core/v1alpha1"
	"github.com/crossplane/crossplane-runtime/pkg/fieldpath"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/oam-kubernetes-runtime/apis/core/v1alpha2"
	"github.com/crossplane/oam-kubernetes-runtime/pkg/oam/util"
)

// Reconcile error strings.
const (
	errFmtApplyWorkload      = "cannot apply workload %q"
	errFmtSetWorkloadRef     = "cannot set trait %q reference to %q"
	errFmtGetTraitDefinition = "cannot find trait definition %q %q %q"
	errFmtApplyTrait         = "cannot apply trait %q %q %q"
	errFmtApplyScope         = "cannot apply scope %q %q %q"
)

// A WorkloadApplicator creates or updates workloads and their traits.
type WorkloadApplicator interface {
	// Apply a workload and its traits.
	Apply(ctx context.Context, status []v1alpha2.WorkloadStatus, w []Workload, ao ...resource.ApplyOption) error
}

// A WorkloadApplyFn creates or updates workloads and their traits.
type WorkloadApplyFn func(ctx context.Context, status []v1alpha2.WorkloadStatus, w []Workload, ao ...resource.ApplyOption) error

// Apply a workload and its traits.
func (fn WorkloadApplyFn) Apply(ctx context.Context, status []v1alpha2.WorkloadStatus, w []Workload, ao ...resource.ApplyOption) error {
	return fn(ctx, status, w, ao...)
}

type workloads struct {
	client    resource.Applicator
	rawClient client.Client
}

func (a *workloads) Apply(ctx context.Context, status []v1alpha2.WorkloadStatus, w []Workload, ao ...resource.ApplyOption) error {
	if len(w) == 0 {
		return errors.New("the application has no component")
	}
	// they are all in the same namespace
	var namespace = w[0].Workload.GetNamespace()
	for _, wl := range w {
		if err := a.client.Apply(ctx, wl.Workload, ao...); err != nil {
			return errors.Wrapf(err, errFmtApplyWorkload, wl.Workload.GetName())
		}
		workloadRef := runtimev1alpha1.TypedReference{
			APIVersion: wl.Workload.GetAPIVersion(),
			Kind:       wl.Workload.GetKind(),
			Name:       wl.Workload.GetName(),
		}

		for _, t := range wl.Traits {
			//  We only patch a TypedReference object to the trait if it asks for it
			trait := t
			if traitDefinition, err := util.FetchTraitDefinition(ctx, a.rawClient, &trait); err == nil {
				workloadRefPath := traitDefinition.Spec.WorkloadRefPath
				if len(workloadRefPath) != 0 {
					if err := fieldpath.Pave(t.UnstructuredContent()).SetValue(workloadRefPath, workloadRef); err != nil {
						return errors.Wrapf(err, errFmtSetWorkloadRef, t.GetName(), wl.Workload.GetName())
					}
				}
			} else {
				return errors.Wrapf(err, errFmtGetTraitDefinition, t.GetAPIVersion(), t.GetKind(), t.GetName())
			}

			if err := a.client.Apply(ctx, &trait, ao...); err != nil {
				return errors.Wrapf(err, errFmtApplyTrait, t.GetAPIVersion(), t.GetKind(), t.GetName())
			}
		}

		for _, s := range wl.Scopes {
			return a.applyScope(ctx, wl, s, workloadRef)
		}
	}

	return a.dereferenceScope(ctx, namespace, status, w)
}

func (a *workloads) dereferenceScope(ctx context.Context, namespace string, status []v1alpha2.WorkloadStatus, w []Workload) error {
	for _, st := range status {
		toBeDeferenced := st.Scopes
		for _, wl := range w {
			if (st.Reference.APIVersion == wl.Workload.GetAPIVersion()) &&
				(st.Reference.Kind == wl.Workload.GetKind()) &&
				(st.Reference.Name == wl.Workload.GetName()) {
				toBeDeferenced = findDereferencedScopes(st.Scopes, wl.Scopes)
			}
		}

		for _, s := range toBeDeferenced {
			if err := a.applyScopeRemoval(ctx, namespace, st, s); err != nil {
				return err
			}
		}
	}

	return nil
}

func findDereferencedScopes(statusScopes []v1alpha2.WorkloadScope, scopes []unstructured.Unstructured) []v1alpha2.WorkloadScope {
	toBeDeferenced := []v1alpha2.WorkloadScope{}
	for _, ss := range statusScopes {
		found := false
		for _, s := range scopes {
			if (s.GetAPIVersion() == ss.Reference.APIVersion) &&
				(s.GetKind() == ss.Reference.Kind) &&
				(s.GetName() == ss.Reference.Name) {
				found = true
				break
			}
		}

		if !found {
			toBeDeferenced = append(toBeDeferenced, ss)
		}
	}

	return toBeDeferenced
}

func (a *workloads) applyScope(ctx context.Context, wl Workload, s unstructured.Unstructured, workloadRef runtimev1alpha1.TypedReference) error {
	var refs []interface{}
	if value, err := fieldpath.Pave(s.UnstructuredContent()).GetValue("spec.workloadRefs"); err == nil {
		refs = value.([]interface{})

		for _, item := range refs {
			ref := item.(map[string]interface{})
			if (workloadRef.APIVersion == ref["apiVersion"]) &&
				(workloadRef.Kind == ref["kind"]) &&
				(workloadRef.Name == ref["name"]) {
				// workloadRef is already present, so no need to add it.
				return nil
			}
		}
	}

	refs = append(refs, workloadRef)
	// TODO(rz): Add workloadRef to ScopeDefinition too
	if err := fieldpath.Pave(s.UnstructuredContent()).SetValue("spec.workloadRefs", refs); err != nil {
		return errors.Wrapf(err, errFmtSetWorkloadRef, s.GetName(), wl.Workload.GetName())
	}

	if err := a.rawClient.Update(ctx, &s); err != nil {
		return errors.Wrapf(err, errFmtApplyScope, s.GetAPIVersion(), s.GetKind(), s.GetName())
	}

	return nil
}

func (a *workloads) applyScopeRemoval(ctx context.Context, namespace string, ws v1alpha2.WorkloadStatus, s v1alpha2.WorkloadScope) error {
	workloadRef := runtimev1alpha1.TypedReference{
		APIVersion: ws.Reference.APIVersion,
		Kind:       ws.Reference.Kind,
		Name:       ws.Reference.Name,
	}

	scopeObject := unstructured.Unstructured{}
	scopeObject.SetAPIVersion(s.Reference.APIVersion)
	scopeObject.SetKind(s.Reference.Kind)
	scopeObjectRef := types.NamespacedName{Namespace: namespace, Name: s.Reference.Name}
	if err := a.rawClient.Get(ctx, scopeObjectRef, &scopeObject); err != nil {
		return errors.Wrapf(err, errFmtApplyScope, s.Reference.APIVersion, s.Reference.Kind, s.Reference.Name)
	}

	if value, err := fieldpath.Pave(scopeObject.UnstructuredContent()).GetValue("spec.workloadRefs"); err == nil {
		refs := value.([]interface{})

		workloadRefIndex := -1
		for i, item := range refs {
			ref := item.(map[string]interface{})
			if (workloadRef.APIVersion == ref["apiVersion"]) &&
				(workloadRef.Kind == ref["kind"]) &&
				(workloadRef.Name == ref["name"]) {
				workloadRefIndex = i
				break
			}
		}

		if workloadRefIndex >= 0 {
			// Remove the element at index i.
			refs[workloadRefIndex] = refs[len(refs)-1]
			refs = refs[:len(refs)-1]

			// TODO(rz): Add workloadRef to ScopeDefinition too
			if err := fieldpath.Pave(scopeObject.UnstructuredContent()).SetValue("spec.workloadRefs", refs); err != nil {
				return errors.Wrapf(err, errFmtSetWorkloadRef, s.Reference.Name, ws.Reference.Name)
			}

			if err := a.rawClient.Update(ctx, &scopeObject); err != nil {
				return errors.Wrapf(err, errFmtApplyScope, s.Reference.APIVersion, s.Reference.Kind, s.Reference.Name)
			}
		}
	}

	return nil
}
