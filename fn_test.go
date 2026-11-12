package main

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"
)

// A composed managed resource. Every MR has spec.forProvider.
const bucket = `{"apiVersion":"s3.aws.m.upbound.io/v1beta1","kind":"Bucket","spec":{"forProvider":{"region":"us-east-2"}}}`

// A composed resource that isn't a managed resource.
const configMap = `{"apiVersion":"v1","kind":"ConfigMap","data":{"key":"value"}}`

// A composed resource that is itself an XR. The composition pins its
// composition under spec.crossplane, the block Crossplane keeps its own fields
// in, and sets an annotation of its own.
const nestedXR = `{"apiVersion":"example.crossplane.io/v1","kind":"Database","metadata":{"name":"my-db","annotations":{"example.org/owner":"team-a"}},"spec":{"crossplane":{"compositionSelector":{"matchLabels":{"tier":"db"}}},"parameters":{"size":"small"}}}`

// nestedXRWith returns the nested XR with the supplied mode annotation added.
func nestedXRWith(mode string) string {
	return `{"apiVersion":"example.crossplane.io/v1","kind":"Database","metadata":{"name":"my-db","annotations":{"example.org/owner":"team-a","lifecycle.fn.crossplane.io/mode":"` + mode + `"}},"spec":{"crossplane":{"compositionSelector":{"matchLabels":{"tier":"db"}}},"parameters":{"size":"small"}}}`
}

// The same XR as a composition that doesn't set spec.crossplane writes it,
// and as Crossplane observes it once it exists.
const (
	nestedXRTemplate = `{"apiVersion":"example.crossplane.io/v1","kind":"Database","metadata":{"name":"my-db"},"spec":{"parameters":{"size":"small"}}}`
	nestedXRObserved = `{"apiVersion":"example.crossplane.io/v1","kind":"Database","metadata":{"name":"my-db","namespace":"default"},"spec":{"crossplane":{"compositionRef":{"name":"databases.example.crossplane.io"},"compositionUpdatePolicy":"Automatic"},"parameters":{"size":"small"}}}`
)

// nestedXRTemplateWith returns the template with the supplied mode annotation.
func nestedXRTemplateWith(mode string) string {
	return `{"apiVersion":"example.crossplane.io/v1","kind":"Database","metadata":{"name":"my-db","annotations":{"lifecycle.fn.crossplane.io/mode":"` + mode + `"}},"spec":{"parameters":{"size":"small"}}}`
}

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

// xrWith returns an observed XR without a mode annotation, plus the supplied
// top-level JSON fields, for example `"spec":{"parameters":{}}`.
func xrWith(fields string) *fnv1.State {
	return &fnv1.State{Composite: &fnv1.Resource{Resource: resource.MustStructJSON(
		`{"apiVersion":"example.crossplane.io/v1","kind":"App","metadata":{"name":"my-app","namespace":"default"},` + fields + `}`,
	)}}
}

// input returns Function input with the supplied JSON fields.
func input(fields string) *structpb.Struct {
	return resource.MustStructJSON(`{"apiVersion":"lifecycle.fn.crossplane.io/v1beta1","kind":"Input",` + fields + `}`)
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
							Message:  "Full (from default): set managementPolicies [*] on 1 managed resources (1 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "Full",
							Message: new("Composed managed resources use managementPolicies [*] (mode from default)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
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
							Message:  "Provision (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe Create Update LateInitialize] on 1 managed resources (1 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "Provision",
							Message: new("Composed managed resources use managementPolicies [Observe Create Update LateInitialize] (mode from annotation lifecycle.fn.crossplane.io/mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
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
							Message:  "ObserveOnly (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "ObserveOnly",
							Message: new("Composed managed resources use managementPolicies [Observe] (mode from annotation lifecycle.fn.crossplane.io/mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
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
							Message:  "Deprovision (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe Create Delete] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "Deprovision",
							Message: new("Composed managed resources use managementPolicies [Observe Create Delete] (mode from annotation lifecycle.fn.crossplane.io/mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"InputMode": {
			reason: "Without an annotation or a modeRef the Function should use the input's mode",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Input:    input(`"mode":"ObserveOnly"`),
					Observed: xr(""),
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
							Message:  "ObserveOnly (from input mode): set managementPolicies [Observe] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "ObserveOnly",
							Message: new("Composed managed resources use managementPolicies [Observe] (mode from input mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"ModeRefFromSpec": {
			reason: "The Function should read the mode from the XR spec field modeRef points to",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Input:    input(`"mode":"ObserveOnly","modeRef":"spec.parameters.managementMode"`),
					Observed: xrWith(`"spec":{"parameters":{"managementMode":"Provision"}}`),
					Desired:  desired(map[string]string{"bucket": bucket}),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"bucket": bucketWith(`["Observe","Create","Update","LateInitialize"]`)}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "Provision (from modeRef spec.parameters.managementMode): set managementPolicies [Observe Create Update LateInitialize] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "Provision",
							Message: new("Composed managed resources use managementPolicies [Observe Create Update LateInitialize] (mode from modeRef spec.parameters.managementMode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"ModeRefFromStatus": {
			reason: "The Function should read the mode from the XR status field modeRef points to",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Input:    input(`"modeRef":"status.lifecycle.mode"`),
					Observed: xrWith(`"status":{"lifecycle":{"mode":"ObserveOnly"}}`),
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
							Message:  "ObserveOnly (from modeRef status.lifecycle.mode): set managementPolicies [Observe] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "ObserveOnly",
							Message: new("Composed managed resources use managementPolicies [Observe] (mode from modeRef status.lifecycle.mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"ModeRefFromContext": {
			reason: "The Function should read the mode from the pipeline context, for example an EnvironmentConfig",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Input:    input(`"modeRef":"context.[apiextensions.crossplane.io/environment].managementMode"`),
					Observed: xr(""),
					Desired:  desired(map[string]string{"bucket": bucket}),
					Context:  resource.MustStructJSON(`{"apiextensions.crossplane.io/environment":{"managementMode":"Deprovision"}}`),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"bucket": bucketWith(`["Observe","Create","Delete"]`)}),
					Context: resource.MustStructJSON(`{"apiextensions.crossplane.io/environment":{"managementMode":"Deprovision"}}`),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "Deprovision (from modeRef context.[apiextensions.crossplane.io/environment].managementMode): set managementPolicies [Observe Create Delete] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "Deprovision",
							Message: new("Composed managed resources use managementPolicies [Observe Create Delete] (mode from modeRef context.[apiextensions.crossplane.io/environment].managementMode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"UnsetModeRefFallsBackToInputMode": {
			reason: "When nothing is set at modeRef the Function should fall back to the input's mode",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Input:    input(`"mode":"ObserveOnly","modeRef":"spec.parameters.managementMode"`),
					Observed: xrWith(`"spec":{"parameters":{}}`),
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
							Message:  "ObserveOnly (from input mode): set managementPolicies [Observe] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "ObserveOnly",
							Message: new("Composed managed resources use managementPolicies [Observe] (mode from input mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"AnnotationBeatsModeRef": {
			reason: "The XR annotation is the operator's break-glass lever and should beat modeRef",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Input: input(`"modeRef":"spec.parameters.managementMode"`),
					Observed: &fnv1.State{Composite: &fnv1.Resource{Resource: resource.MustStructJSON(`{
						"apiVersion": "example.crossplane.io/v1",
						"kind": "App",
						"metadata": {"name": "my-app", "annotations": {"lifecycle.fn.crossplane.io/mode": "ObserveOnly"}},
						"spec": {"parameters": {"managementMode": "Provision"}}
					}`)}},
					Desired: desired(map[string]string{"bucket": bucket}),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"bucket": bucketWith(`["Observe"]`)}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "ObserveOnly (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe] on 1 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "ObserveOnly",
							Message: new("Composed managed resources use managementPolicies [Observe] (mode from annotation lifecycle.fn.crossplane.io/mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"InvalidModeRefIsFatal": {
			reason: "A modeRef that doesn't start with spec., status., or context. should return a fatal result",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Input:    input(`"modeRef":"metadata.name"`),
					Observed: xr(""),
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
							Message:  `modeRef "metadata.name" must be a spec., status., or context. path`,
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
							Message:  `invalid mode from annotation lifecycle.fn.crossplane.io/mode: unknown mode "Orphan": must be one of Full, Provision, ObserveOnly, Deprovision`,
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"NestedXRGetsTheModeAnnotation": {
			reason: "A composed XR should get the mode annotation and keep its own annotations, the bucket its policies, and the ConfigMap nothing",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Observed: xr("Provision"),
					Desired:  desired(map[string]string{"bucket": bucket, "settings": configMap, "db": nestedXR}),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"bucket": bucketWith(`["Observe","Create","Update","LateInitialize"]`), "settings": configMap, "db": nestedXRWith("Provision")}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "Provision (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe Create Update LateInitialize] on 1 managed resources (1 skipped), mode annotation on 1 nested XRs",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "Provision",
							Message: new("Composed managed resources use managementPolicies [Observe Create Update LateInitialize] (mode from annotation lifecycle.fn.crossplane.io/mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
				},
			},
		},
		"ObservedNestedXRGetsTheModeAnnotation": {
			reason: "When the composition doesn't set spec.crossplane on a nested XR, the observed resource tells it apart from a plain object",
			args: args{
				req: &fnv1.RunFunctionRequest{
					Observed: &fnv1.State{
						Composite: xr("ObserveOnly").GetComposite(),
						Resources: map[string]*fnv1.Resource{"db": {Resource: resource.MustStructJSON(nestedXRObserved)}},
					},
					Desired: desired(map[string]string{"db": nestedXRTemplate}),
				},
			},
			want: want{
				rsp: &fnv1.RunFunctionResponse{
					Meta:    &fnv1.ResponseMeta{Ttl: durationpb.New(response.DefaultTTL)},
					Desired: desired(map[string]string{"db": nestedXRTemplateWith("ObserveOnly")}),
					Results: []*fnv1.Result{
						{
							Severity: fnv1.Severity_SEVERITY_NORMAL,
							Message:  "ObserveOnly (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe] on 0 managed resources (0 skipped), mode annotation on 1 nested XRs",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "ObserveOnly",
							Message: new("Composed managed resources use managementPolicies [Observe] (mode from annotation lifecycle.fn.crossplane.io/mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
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
							Message:  "ObserveOnly (from annotation lifecycle.fn.crossplane.io/mode): set managementPolicies [Observe] on 0 managed resources (0 skipped)",
							Target:   fnv1.Target_TARGET_COMPOSITE.Enum(),
						},
					},
					Conditions: []*fnv1.Condition{
						{
							Type:    "LifecycleMode",
							Status:  fnv1.Status_STATUS_CONDITION_TRUE,
							Reason:  "ObserveOnly",
							Message: new("Composed managed resources use managementPolicies [Observe] (mode from annotation lifecycle.fn.crossplane.io/mode)"),
							Target:  fnv1.Target_TARGET_COMPOSITE.Enum(),
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
