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

// A composed managed resource. Every MR has spec.forProvider.
const bucket = `{"apiVersion":"s3.aws.m.upbound.io/v1beta1","kind":"Bucket","spec":{"forProvider":{"region":"us-east-2"}}}`

// A composed resource that isn't a managed resource.
const configMap = `{"apiVersion":"v1","kind":"ConfigMap","data":{"key":"value"}}`

// bucketWith returns the bucket with the supplied JSON management policies.
func bucketWith(policies string) string {
	return `{"apiVersion":"s3.aws.m.upbound.io/v1beta1","kind":"Bucket","spec":{"forProvider":{"region":"us-east-2"},"managementPolicies":` + policies + `}}`
}

// xr returns an observed XR. An empty mode means no mode annotation.
func xr(mode string) *fnv1.State {
	annotations := ""
	if mode != "" {
		annotations = `,"annotations":{"lifecycle.fn.crossplane.io/mode":"` + mode + `"}`
	}
	return &fnv1.State{Composite: &fnv1.Resource{Resource: resource.MustStructJSON(
		`{"apiVersion":"example.crossplane.io/v1","kind":"App","metadata":{"name":"my-app","namespace":"default"` + annotations + `}}`,
	)}}
}

// desired returns desired state containing the supplied composed resources.
func desired(resources map[string]string) *fnv1.State {
	s := &fnv1.State{Resources: map[string]*fnv1.Resource{}}
	for name, r := range resources {
		s.Resources[name] = &fnv1.Resource{Resource: resource.MustStructJSON(r)}
	}
	return s
}

//nolint:goconst // Table tests spell out expected values on purpose.
func TestRunFunction(t *testing.T) {
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
		"NoAnnotationDefaultsToFull": {
			reason: "Without a mode annotation the Function should set the Full policies on managed resources",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Observed: xr(""),
					Desired:  desired(map[string]string{"bucket": bucket, "settings": configMap}),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"bucket": bucketWith(`["*"]`), "settings": configMap}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "Full: set managementPolicies [*] on 1 managed resources (1 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"Provision": {
			reason: "The Provision mode should drop the Delete policy so deleting the XR orphans external resources",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Observed: xr("Provision"),
					Desired:  desired(map[string]string{"bucket": bucket, "settings": configMap}),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"bucket": bucketWith(`["Observe","Create","Update","LateInitialize"]`), "settings": configMap}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "Provision: set managementPolicies [Observe Create Update LateInitialize] on 1 managed resources (1 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"ObserveOnly": {
			reason: "The ObserveOnly mode should only allow Crossplane to observe managed resources",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Observed: xr("ObserveOnly"),
					Desired:  desired(map[string]string{"bucket": bucket}),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"bucket": bucketWith(`["Observe"]`)}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "ObserveOnly: set managementPolicies [Observe] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"Deprovision": {
			reason: "The Deprovision mode should drop the Update policy so drift is torn down rather than reconciled",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Observed: xr("Deprovision"),
					Desired:  desired(map[string]string{"bucket": bucket}),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"bucket": bucketWith(`["Observe","Create","Delete"]`)}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "Deprovision: set managementPolicies [Observe Create Delete] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"UnknownModeIsFatal": {
			reason: "An unknown mode should return a fatal result and leave the desired state untouched",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Observed: xr("Orphan"),
					Desired:  desired(map[string]string{"bucket": bucket}),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"bucket": bucket}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_FATAL,
							Message:  `invalid lifecycle.fn.crossplane.io/mode annotation: unknown mode "Orphan": must be one of Full, Provision, ObserveOnly, Deprovision`,
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"NoDesiredResources": {
			reason: "The Function should succeed when previous steps produced no composed resources",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Observed: xr("ObserveOnly"),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: &fnv1.State{},
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "ObserveOnly: set managementPolicies [Observe] on 0 managed resources (0 skipped)",
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
