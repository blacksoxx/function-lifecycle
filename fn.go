package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/crossplane/crossplane-runtime/v2/pkg/fieldpath"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/crossplane/function-sdk-go/errors"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"

	"github.com/crossplane/function-lifecycle/input/v1beta1"
)

const (
	// AnnotationMode on the XR selects the lifecycle mode of its composed
	// resources.
	AnnotationMode = "lifecycle.fn.crossplane.io/mode"

	// ConditionTypeLifecycleMode reports the active mode on the XR.
	ConditionTypeLifecycleMode = "LifecycleMode"

	// ContextKeyDecisions is where the function records what it decided for
	// each composed resource when the input's debug is true.
	ContextKeyDecisions = "lifecycle.fn.crossplane.io/decisions"
)

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

	// An Operation has no XR. There the function sets the mode annotation on
	// the resources the Operation requires instead. See operation.go.
	if req.GetObserved().GetComposite() == nil {
		return f.runOperation(req, rsp), nil
	}

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

	mode, source, err := resolveMode(req, xr, in)
	if err != nil {
		response.Fatal(rsp, err)
		return rsp, nil
	}

	// Fail closed: a typo in the mode must never fall back to Full.
	policies, err := mode.Policies()
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "invalid mode from %s", source))
		return rsp, nil
	}
	f.log.Debug("Resolved lifecycle mode", "mode", mode, "source", source, "policies", policies)

	desired, err := request.GetDesiredComposedResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot get desired composed resources"))
		return rsp, nil
	}

	var applied, skipped int
	decisions := map[string]any{}
	for name, dr := range desired {
		if !isManaged(dr) {
			f.log.Debug("Skipping composed resource that is not a managed resource", "resource", name)
			decisions[string(name)] = "skipped: not a managed resource"
			skipped++
			continue
		}
		if err := dr.Resource.SetValue("spec.managementPolicies", policies); err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot set spec.managementPolicies on composed resource %q", name))
			return rsp, nil
		}
		f.log.Debug("Set management policies", "resource", name, "policies", policies)
		decisions[string(name)] = policies
		applied++
	}

	if in.Debug {
		if err := setDecisions(rsp, decisions); err != nil {
			response.Fatal(rsp, err)
			return rsp, nil
		}
	}

	if err := response.SetDesiredComposedResources(rsp, desired); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot set desired composed resources"))
		return rsp, nil
	}

	// Results become events on the XR. Conditions stay on the XR's status, so
	// kubectl get and crossplane beta trace show the active mode.
	response.Normalf(rsp, "%s (from %s): set managementPolicies %v on %d managed resources (%d skipped)", mode, source, policies, applied, skipped)
	response.ConditionTrue(rsp, ConditionTypeLifecycleMode, string(mode)).
		WithMessage(fmt.Sprintf("Composed managed resources use managementPolicies %v (mode from %s)", policies, source)).
		TargetComposite()

	return rsp, nil
}

// resolveMode returns the lifecycle mode and where it came from. In order of
// precedence it comes from the XR's mode annotation, the field ModeRef points
// to, the input's mode, or defaults to Full.
func resolveMode(req *fnv1.RunFunctionRequest, xr *resource.Composite, in *v1beta1.Input) (Mode, string, error) {
	if v, ok := xr.Resource.GetAnnotations()[AnnotationMode]; ok {
		return Mode(v), "annotation " + AnnotationMode, nil
	}
	if in.ModeRef != "" {
		v, err := lookup(req, xr, in.ModeRef)
		if err != nil {
			return "", "", err
		}
		if v != "" {
			return Mode(v), "modeRef " + in.ModeRef, nil
		}
	}
	if in.Mode != "" {
		return Mode(in.Mode), "input mode", nil
	}
	return ModeFull, "default", nil
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
		v, err = fieldpath.Pave(req.GetContext().AsMap()).GetString(strings.TrimPrefix(ref, "context."))
	default:
		return "", errors.Errorf("modeRef %q must be a spec., status., or context. path", ref)
	}
	if fieldpath.IsNotFound(err) {
		return "", nil
	}
	return v, errors.Wrapf(err, "cannot resolve modeRef %q", ref)
}

// setDecisions writes the decision for each composed resource to the pipeline
// context, so later steps and crossplane composition render -c can see it.
// Policies arrive as []string, which structpb only accepts as []any.
func setDecisions(rsp *fnv1.RunFunctionResponse, decisions map[string]any) error {
	for name, d := range decisions {
		if policies, ok := d.([]string); ok {
			actions := make([]any, len(policies))
			for i, p := range policies {
				actions[i] = p
			}
			decisions[name] = actions
		}
	}
	v, err := structpb.NewValue(decisions)
	if err != nil {
		return errors.Wrap(err, "cannot convert decisions to a context value")
	}
	response.SetContextKey(rsp, ContextKeyDecisions, v)
	return nil
}

// isManaged returns true if the composed resource looks like a managed
// resource. Every managed resource has spec.forProvider. Plain Kubernetes
// objects and nested XRs don't.
func isManaged(dr *resource.DesiredComposed) bool {
	_, err := dr.Resource.GetValue("spec.forProvider")
	return err == nil
}
