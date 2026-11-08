package main

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"
)

// app returns an XR the way an Operation's required resources include it.
func app(name string) *fnv1.Resource {
	return &fnv1.Resource{Resource: resource.MustStructJSON(
		`{"apiVersion":"example.crossplane.io/v1","kind":"App","metadata":{"name":"` + name + `","namespace":"default","labels":{"freeze":"true"}},"spec":{"parameters":{"region":"us-west-2"}}}`,
	)}
}

// appPatch returns the server-side apply patch that sets the mode annotation
// on the named App.
func appPatch(name, mode string) string {
	return `{"apiVersion":"example.crossplane.io/v1","kind":"App","metadata":{"name":"` + name + `","namespace":"default","annotations":{"lifecycle.fn.crossplane.io/mode":"` + mode + `"}}}`
}

func TestRunOperation(t *testing.T) {
	type args struct {
		ctx context.Context
		req *fnv1.RunFunctionRequest
	}
	type want struct {
		rsp *fnv1.RunFunctionResponse
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SetsModeAnnotation": {
			reason: "In an Operation the Function should set the mode annotation on every required resource, report them in its output, and change nothing else",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Input: input(`"mode":"ObserveOnly"`),
					RequiredResources: map[string]*fnv1.Resources{
						"apps": {Items: []*fnv1.Resource{app("payments"), app("checkout")}},
					},
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta: &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{
						"default/payments": appPatch("payments", "ObserveOnly"),
						"default/checkout": appPatch("checkout", "ObserveOnly"),
					}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "Set lifecycle.fn.crossplane.io/mode to ObserveOnly on 2 resources",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Output: resource.MustStructJSON(`{"mode":"ObserveOnly","resources":["default/checkout","default/payments"]}`),
				},
			},
		},
		"NoRequiredResources": {
			reason: "An Operation that matches no resources should change nothing",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Input: input(`"mode":"Full"`),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: &fnv1.State{Resources: map[string]*fnv1.Resource{}},
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "Set lifecycle.fn.crossplane.io/mode to Full on 0 resources",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Output: resource.MustStructJSON(`{"mode":"Full","resources":[]}`),
				},
			},
		},
		"MissingModeIsFatal": {
			reason: "An Operation has no XR to read the mode from, so its input must set one",
			args: args{
				req: &fnv1.RunFunctionRequest{
					RequiredResources: map[string]*fnv1.Resources{
						"apps": {Items: []*fnv1.Resource{app("payments")}},
					},
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta: &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_FATAL,
							Message:  `invalid input mode for an Operation: unknown mode "": must be one of Full, Provision, ObserveOnly, Deprovision`,
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &Function{log: logging.NewNopLogger()}
			rsp, err := f.RunFunction(tc.args.ctx, tc.args.req)

			if diff := cmp.Diff(tc.want.rsp, rsp, protocmp.Transform()); diff != "" {
				t.Errorf("%s\nf.RunFunction(...): -want rsp, +got rsp:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nf.RunFunction(...): -want err, +got err:\n%s", tc.reason, diff)
			}
		})
	}
}
