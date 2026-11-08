package main

import (
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/function-sdk-go/errors"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"

	"github.com/crossplane/function-lifecycle/input/v1beta1"
)

// operationOutput is what the function reports in the Operation's
// status.pipeline[].output: the mode it set and the resources it set it on.
type operationOutput struct {
	Mode      string   `json:"mode"`
	Resources []string `json:"resources"`
}

// runOperation runs the function as a step of an Operation, CronOperation, or
// WatchOperation. There's no XR there. Instead it sets the mode annotation on
// every resource the Operation requires, for example every XR with a
// freeze: "true" label. The composition step then applies that mode the next
// time each XR reconciles.
func (f *Function) runOperation(req *fnv1.RunFunctionRequest, rsp *fnv1.RunFunctionResponse) *fnv1.RunFunctionResponse {
	in := &v1beta1.Input{}
	if req.GetInput() != nil {
		if err := request.GetInput(req, in); err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot get Function input from %T", req))
			return rsp
		}
	}

	// Fail closed: never set a mode the composition step would reject.
	mode := Mode(in.Mode)
	if _, err := mode.Policies(); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "invalid input mode for an Operation"))
		return rsp
	}

	required, err := request.GetRequiredResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot get required resources"))
		return rsp
	}

	desired := map[resource.Name]*unstructured.Unstructured{}
	for _, rs := range required {
		for _, r := range rs {
			// Crossplane applies desired resources with server-side apply, so
			// send only what identifies the resource and the fields this
			// function owns.
			patch := &unstructured.Unstructured{}
			patch.SetAPIVersion(r.Resource.GetAPIVersion())
			patch.SetKind(r.Resource.GetKind())
			patch.SetNamespace(r.Resource.GetNamespace())
			patch.SetName(r.Resource.GetName())
			patch.SetAnnotations(map[string]string{AnnotationMode: string(mode)})
			desired[resource.Name(r.Resource.GetNamespace()+"/"+r.Resource.GetName())] = patch
		}
	}

	if err := response.SetDesiredResources(rsp, desired); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot set desired resources"))
		return rsp
	}

	out := operationOutput{Mode: string(mode), Resources: make([]string, 0, len(desired))}
	for name := range desired {
		out.Resources = append(out.Resources, string(name))
	}
	sort.Strings(out.Resources)
	if err := response.SetOutput(rsp, out); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot set output"))
		return rsp
	}

	f.log.Debug("Set the mode annotation", "mode", mode, "resources", len(desired))
	response.Normalf(rsp, "Set %s to %s on %d resources", AnnotationMode, mode, len(desired))
	return rsp
}
