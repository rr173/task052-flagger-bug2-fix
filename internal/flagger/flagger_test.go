package flagger

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestValidateTypes(t *testing.T) {
	cases := []struct {
		name    string
		flag    Flag
		wantErr error
	}{
		{
			name: "bool ok",
			flag: Flag{Key: "f", Type: TypeBool, Default: false},
		},
		{
			name:    "bool default wrong type",
			flag:    Flag{Key: "f", Type: TypeBool, Default: "yes"},
			wantErr: ErrDefaultType,
		},
		{
			name: "number ok",
			flag: Flag{Key: "f", Type: TypeNumber, Default: 10},
		},
		{
			name:    "number default wrong type",
			flag:    Flag{Key: "f", Type: TypeNumber, Default: "x"},
			wantErr: ErrDefaultType,
		},
		{
			name: "string ok",
			flag: Flag{Key: "f", Type: TypeString, Default: "v"},
		},
		{
			name:    "string default wrong type",
			flag:    Flag{Key: "f", Type: TypeString, Default: 3},
			wantErr: ErrDefaultType,
		},
		{
			name:    "empty key",
			flag:    Flag{Key: "", Type: TypeBool, Default: true},
			wantErr: ErrEmptyKey,
		},
		{
			name:    "unknown type",
			flag:    Flag{Key: "f", Type: "weird", Default: true},
			wantErr: ErrUnknownType,
		},
		{
			name: "rule value wrong type",
			flag: Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
				{Value: "notbool"},
			}},
			wantErr: ErrRuleValueType,
		},
		{
			name: "unknown op rejected",
			flag: Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
				{Value: true, Conditions: []Condition{{Attribute: "a", Op: "matches"}}},
			}},
			wantErr: ErrUnknownOp,
		},
		{
			name: "eq value array rejected",
			flag: Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
				{Value: true, Conditions: []Condition{{Attribute: "a", Op: OpEq, Value: []any{"x"}}}},
			}},
			wantErr: ErrCondNotScalar,
		},
		{
			name: "in value scalar rejected",
			flag: Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
				{Value: true, Conditions: []Condition{{Attribute: "a", Op: OpIn, Value: "x"}}},
			}},
			wantErr: ErrCondNotArray,
		},
		{
			name: "gt value non-number rejected",
			flag: Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
				{Value: true, Conditions: []Condition{{Attribute: "a", Op: OpGt, Value: "x"}}},
			}},
			wantErr: ErrCondNotNumber,
		},
		{
			name: "percentage out of range",
			flag: Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
				{Value: true, Rollout: &Rollout{Percentage: 101, BucketBy: "u"}},
			}},
			wantErr: ErrPctOutOfRange,
		},
		{
			name: "empty bucketBy rejected",
			flag: Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
				{Value: true, Rollout: &Rollout{Percentage: 50, BucketBy: ""}},
			}},
			wantErr: ErrEmptyBucketBy,
		},
		{
			name: "empty attribute rejected",
			flag: Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
				{Value: true, Conditions: []Condition{{Attribute: "", Op: OpEq, Value: "x"}}},
			}},
			wantErr: ErrEmptyAttr,
		},
		{
			name: "full valid flag",
			flag: Flag{Key: "f", Type: TypeNumber, Default: 10, Rules: []Rule{
				{Value: 100, Conditions: []Condition{{Attribute: "v", Op: OpGte, Value: 2.0}}},
				{Value: 50, Rollout: &Rollout{Percentage: 50, BucketBy: "u"}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(&tc.flag)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestEvaluateDefault(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeBool, Default: false}
	ctx := &Context{Attributes: map[string]any{"x": 1}}
	got := Evaluate(f, ctx)
	if got.Value != false {
		t.Errorf("value = %v, want false", got.Value)
	}
	if got.Reason != "default" {
		t.Errorf("reason = %q, want default", got.Reason)
	}
	if got.Matched {
		t.Errorf("matched = true, want false")
	}
	if got.Bucket != nil {
		t.Errorf("bucket = %v, want nil", *got.Bucket)
	}
}

func TestEvaluateUnconditionalRule(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{{Value: true}}}
	ctx := &Context{Attributes: map[string]any{}}
	got := Evaluate(f, ctx)
	if got.Value != true {
		t.Errorf("value = %v, want true", got.Value)
	}
	if got.Reason != "rule-0" {
		t.Errorf("reason = %q, want rule-0", got.Reason)
	}
	if !got.Matched {
		t.Errorf("matched = false, want true")
	}
	if got.Bucket != nil {
		t.Errorf("bucket = %v, want nil (no rollout)", *got.Bucket)
	}
}

func TestEvaluateConditionOps(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeNumber, Default: 0, Rules: []Rule{
		{Value: 1, Conditions: []Condition{{Attribute: "country", Op: OpEq, Value: "US"}}},
		{Value: 2, Conditions: []Condition{{Attribute: "country", Op: OpIn, Value: []any{"CA", "MX"}}}},
		{Value: 3, Conditions: []Condition{{Attribute: "v", Op: OpGte, Value: 2.0}}},
	}}
	cases := []struct {
		ctx   map[string]any
		want  any
		reas  string
	}{
		{map[string]any{"country": "US"}, 1, "rule-0"},
		{map[string]any{"country": "CA"}, 2, "rule-1"},
		{map[string]any{"country": "GB", "v": 3}, 3, "rule-2"},
		{map[string]any{"country": "GB", "v": 1}, 0, "default"},
		{map[string]any{"country": 3}, 0, "default"}, // type mismatch eq
	}
	for i, c := range cases {
		got := Evaluate(f, &Context{Attributes: c.ctx})
		if got.Value != c.want {
			t.Errorf("case %d: value = %v, want %v", i, got.Value, c.want)
		}
		if got.Reason != c.reas {
			t.Errorf("case %d: reason = %q, want %q", i, got.Reason, c.reas)
		}
	}
}

func TestEvaluateMissingAttributeNotSatisfied(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Conditions: []Condition{{Attribute: "missing", Op: OpEq, Value: "x"}}},
	}}
	got := Evaluate(f, &Context{Attributes: map[string]any{}})
	if got.Matched {
		t.Errorf("matched = true, want false for missing attr")
	}
	if got.Reason != "default" {
		t.Errorf("reason = %q, want default", got.Reason)
	}
}

func TestEvaluateExists(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Conditions: []Condition{{Attribute: "v", Op: OpExists}}},
	}}
	got := Evaluate(f, &Context{Attributes: map[string]any{"v": 1}})
	if !got.Matched {
		t.Fatalf("expected match for exists")
	}
	got = Evaluate(f, &Context{Attributes: map[string]any{}})
	if got.Matched {
		t.Fatalf("expected no match for missing attr exists")
	}
}

func TestRolloutDeterministicAndSticky(t *testing.T) {
	f := &Flag{Key: "rollout-flag", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Rollout: &Rollout{Percentage: 50, BucketBy: "userId"}},
	}}
	ctx := &Context{Attributes: map[string]any{"userId": "u-42"}}
	r1 := Evaluate(f, ctx)
	r2 := Evaluate(f, ctx)
	if r1.Value != r2.Value || r1.Reason != r2.Reason {
		t.Fatalf("non-deterministic: r1=%+v r2=%+v", r1, r2)
	}
	if r1.Bucket == nil {
		t.Fatalf("bucket nil for rollout")
	}
	if *r1.Bucket != *r2.Bucket {
		t.Fatalf("bucket differs: %d vs %d", *r1.Bucket, *r2.Bucket)
	}
	// Independent of map iteration order: rebuild the context map and re-eval.
	ctx2 := &Context{Attributes: map[string]any{"userId": "u-42", "extra": "noise"}}
	r3 := Evaluate(f, ctx2)
	if *r3.Bucket != *r1.Bucket {
		t.Fatalf("bucket changed with map contents: %d vs %d", *r3.Bucket, *r1.Bucket)
	}
}

func TestRolloutZeroAlwaysFallsThrough(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Conditions: []Condition{{Attribute: "country", Op: OpEq, Value: "US"}},
			Rollout: &Rollout{Percentage: 0, BucketBy: "userId"}},
		{Value: false, Conditions: nil},
	}}
	ctx := &Context{Attributes: map[string]any{"country": "US", "userId": "u1"}}
	got := Evaluate(f, ctx)
	if got.Value != false {
		t.Errorf("value = %v, want false (0%% rollout must fall through)", got.Value)
	}
	if got.Reason != "rule-1" {
		t.Errorf("reason = %q, want rule-1", got.Reason)
	}
	if got.Bucket == nil {
		t.Errorf("bucket nil, want the 0%% rollout bucket")
	}
}

func TestRolloutHundredAlwaysWins(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Rollout: &Rollout{Percentage: 100, BucketBy: "userId"}},
	}}
	for i := 0; i < 200; i++ {
		ctx := &Context{Attributes: map[string]any{"userId": fmt.Sprintf("u-%d", i)}}
		got := Evaluate(f, ctx)
		if got.Value != true || got.Reason != "rule-0" {
			t.Fatalf("u-%d: 100%% rollout must always win, got %+v", i, got)
		}
	}
}

func TestRolloutDistribution(t *testing.T) {
	f := &Flag{Key: "dist", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Rollout: &Rollout{Percentage: 50, BucketBy: "userId"}},
	}}
	hits := 0
	for i := 0; i < 1000; i++ {
		ctx := &Context{Attributes: map[string]any{"userId": fmt.Sprintf("user-%d", i)}}
		if Evaluate(f, ctx).Matched {
			hits++
		}
	}
	// 1000 keys at 50% should land close to 500; allow a wide band for hash
	// variance.
	if hits < 400 || hits > 600 {
		t.Fatalf("50%% rollout over 1000 keys got %d hits, want 400..600", hits)
	}
}

func TestAnonymousBucketFallbackStable(t *testing.T) {
	f := &Flag{Key: "anon", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Rollout: &Rollout{Percentage: 50, BucketBy: "userId"}},
	}}
	// No userId in context: bucket falls back to the flag key.
	a := Evaluate(f, &Context{Attributes: map[string]any{}})
	b := Evaluate(f, &Context{Attributes: map[string]any{}})
	if a.Bucket == nil || b.Bucket == nil {
		t.Fatalf("bucket nil for anonymous context")
	}
	if *a.Bucket != *b.Bucket {
		t.Fatalf("anonymous bucket not stable: %d vs %d", *a.Bucket, *b.Bucket)
	}
	// Two distinct anonymous contexts must share the flag-key bucket.
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("anonymous evals differ: %+v vs %+v", a, b)
	}
}

func TestRuleOrderFirstMatchWins(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeNumber, Default: 0, Rules: []Rule{
		{Value: 1, Conditions: []Condition{{Attribute: "x", Op: OpGte, Value: 1.0}}},
		{Value: 2, Conditions: []Condition{{Attribute: "x", Op: OpGte, Value: 100.0}}},
	}}
	got := Evaluate(f, &Context{Attributes: map[string]any{"x": 200.0}})
	if got.Value != 1 {
		t.Errorf("value = %v, want 1 (first match wins)", got.Value)
	}
	if got.Reason != "rule-0" {
		t.Errorf("reason = %q, want rule-0", got.Reason)
	}
}

func TestBucketRange(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Rollout: &Rollout{Percentage: 50, BucketBy: "userId"}},
	}}
	for i := 0; i < 500; i++ {
		ctx := &Context{Attributes: map[string]any{"userId": fmt.Sprintf("k-%d", i)}}
		b := Evaluate(f, ctx).Bucket
		if b == nil {
			t.Fatalf("bucket nil")
		}
		if *b < 0 || *b >= bucketSpace {
			t.Fatalf("bucket %d out of [0,%d)", *b, bucketSpace)
		}
	}
}

func TestNumericCompareTypeMismatch(t *testing.T) {
	// String attribute with a gt condition: not satisfied (non-number attr).
	f := &Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Conditions: []Condition{{Attribute: "s", Op: OpGt, Value: 1.0}}},
	}}
	got := Evaluate(f, &Context{Attributes: map[string]any{"s": "abc"}})
	if got.Matched {
		t.Errorf("gt on string attr should not match")
	}
}

func TestNeTypeMismatch(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Conditions: []Condition{{Attribute: "s", Op: OpNe, Value: 5.0}}},
	}}
	// s="abc" vs 5.0: types differ -> not equal -> ne is true -> match.
	got := Evaluate(f, &Context{Attributes: map[string]any{"s": "abc"}})
	if !got.Matched {
		t.Errorf("ne with type mismatch should match (values are not equal)")
	}
	// s=5.0 vs 5.0: equal -> ne false -> no match.
	got = Evaluate(f, &Context{Attributes: map[string]any{"s": 5.0}})
	if got.Matched {
		t.Errorf("ne with equal values should not match")
	}
}
