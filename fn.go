package main

import (
	"context"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"

	"github.com/crossplane/function-sdk-go/errors"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"

	"github.com/crossplane/function-lifecycle/input/v1beta1"
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

	// The input is optional. Without it we rely on the annotation alone.
	in := &v1beta1.Input{}
	if req.GetInput() != nil {
		if err := request.GetInput(req, in); err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot get Function input from %T", req))
			return rsp, nil
		}
	}

	mode, err := resolveMode(req, xr, in)
	if err != nil {
		response.Fatal(rsp, err)
		return rsp, nil
	}

	// Fail closed: a typo in the mode must never fall back to Full.
	policies, err := mode.Policies()
	if err != nil {
		response.Fatal(rsp, err)
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

// resolveMode returns the lifecycle mode. In order of precedence it comes from
// the XR's mode annotation, the field ModeRef points to, the input's mode, or
// defaults to Full.
func resolveMode(req *fnv1.RunFunctionRequest, xr *resource.Composite, in *v1beta1.Input) (Mode, error) {
	// TODO(lab-03) 1: if the XR has the AnnotationMode annotation, return
	// Mode(value), nil. It's the same lookup as in Lab 2.

	// TODO(lab-03) 2: if in.ModeRef isn't empty, call
	// v, err := lookup(req, xr, in.ModeRef). If err isn't nil, return "", err.
	// If v isn't empty, return Mode(v), nil.

	// TODO(lab-03) 3: if in.Mode isn't empty, return Mode(in.Mode), nil.

	return ModeFull, nil
}

// lookup returns the string at the supplied spec., status., or context. path.
// It returns an empty string if nothing is set at the path.
func lookup(req *fnv1.RunFunctionRequest, xr *resource.Composite, ref string) (string, error) {
	var v string
	var err error
	switch {
	case strings.HasPrefix(ref, "spec."), strings.HasPrefix(ref, "status."):
		v, err = xr.Resource.GetString(ref)
	case strings.HasPrefix(ref, "context."):
		// TODO(lab-03) 4: read the value from the pipeline context, a map that
		// every step in the pipeline can read and write.
		// req.GetContext().AsMap() returns it. Wrap that map with
		// fieldpath.Pave(), then call GetString() with the path after
		// "context.", which is strings.TrimPrefix(ref, "context.").
		// Assign the result to v, err, like the case above.
	default:
		return "", errors.Errorf("modeRef %q must be a spec., status., or context. path", ref)
	}
	if fieldpath.IsNotFound(err) {
		return "", nil
	}
	return v, errors.Wrapf(err, "cannot resolve modeRef %q", ref)
}

// isManaged returns true if the composed resource looks like a managed
// resource. Every managed resource has spec.forProvider. Plain Kubernetes
// objects and nested XRs don't.
func isManaged(dr *resource.DesiredComposed) bool {
	_, err := dr.Resource.GetValue("spec.forProvider")
	return err == nil
}
