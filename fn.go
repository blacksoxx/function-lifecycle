package main

import (
	"context"

	"github.com/crossplane/function-sdk-go/errors"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"
)

// AnnotationMode on the XR selects the lifecycle mode of its composed
// resources.
const AnnotationMode = "lifecycle.fn.crossplane.io/mode"

// Function sets spec.managementPolicies on composed managed resources.
type Function struct {
	fnv1.UnimplementedFunctionRunnerServiceServer

	log logging.Logger
}

// RunFunction runs the Function.
func (f *Function) RunFunction(_ context.Context, req *fnv1.RunFunctionRequest) (*fnv1.RunFunctionResponse, error) {
	f.log.Info("Running function", "tag", req.GetMeta().GetTag())

	// response.To copies the desired state accumulated by previous pipeline
	// steps into our response. We only change what we need to.
	rsp := response.To(req, response.DefaultTTL)

	xr, err := request.GetObservedCompositeResource(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot get observed composite resource"))
		return rsp, nil
	}

	mode := ModeFull
	if v, ok := xr.Resource.GetAnnotations()[AnnotationMode]; ok {
		mode = Mode(v)
	}

	// Fail closed: a typo in the mode must never fall back to Full.
	policies, err := mode.Policies()
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "invalid %s annotation", AnnotationMode))
		return rsp, nil
	}

	desired, err := request.GetDesiredComposedResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot get desired composed resources"))
		return rsp, nil
	}

	var applied, skipped int
	for name, dr := range desired {
		if !isManaged(dr) {
			f.log.Debug("Skipping composed resource that is not a managed resource", "resource", name)
			skipped++
			continue
		}
		if err := dr.Resource.SetValue("spec.managementPolicies", policies); err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot set spec.managementPolicies on composed resource %q", name))
			return rsp, nil
		}
		applied++
	}

	if err := response.SetDesiredComposedResources(rsp, desired); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot set desired composed resources"))
		return rsp, nil
	}

	response.Normalf(rsp, "%s: set managementPolicies %v on %d managed resources (%d skipped)", mode, policies, applied, skipped)

	return rsp, nil
}

// isManaged returns true if the composed resource looks like a managed
// resource. Every managed resource has spec.forProvider. Plain Kubernetes
// objects and nested XRs don't.
func isManaged(dr *resource.DesiredComposed) bool {
	_, err := dr.Resource.GetValue("spec.forProvider")
	return err == nil
}
