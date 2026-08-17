// Package selfcheck runs an end-to-end verification of the feature flag
// service's core logic. It is invoked by the --smoke-test flag and exits the
// process on completion. It exercises the flagger engine and registry packages
// directly (no network) so the check is deterministic and fast. Each scenario
// builds a fresh registry so global state never leaks between checks.
package selfcheck

import (
	"errors"
	"fmt"
	"sync"

	"task052-flagger/internal/flagger"
	"task052-flagger/internal/registry"
)

// Run exercises the validation, evaluation, rollout, type-safety, and
// accounting behaviors across isolated scenarios, returning nil if every
// behavior matches the specification.
func Run() error {
	scenarios := []struct {
		name string
		fn   func() error
	}{
		{"默认值求值", scenarioDefault},
		{"无条件规则命中", scenarioUnconditionalRule},
		{"条件操作符覆盖", scenarioConditionOps},
		{"缺失属性条件不满足", scenarioMissingAttr},
		{"exists操作符", scenarioExists},
		{"等值类型不一致判不等", scenarioEqTypeMismatch},
		{"比较操作符非数字不满足", scenarioCompareNonNumber},
		{"规则顺序首匹配", scenarioFirstMatchWins},
		{"百分比确定性粘性", scenarioRolloutSticky},
		{"百分比与map无关", scenarioRolloutMapIndependent},
		{"0百分比必穿透", scenarioRolloutZeroFallThrough},
		{"100百分比必命中", scenarioRolloutHundred},
		{"百分比分布合理", scenarioRolloutDistribution},
		{"匿名上下文稳定分桶", scenarioAnonymousBucket},
		{"桶号范围合法", scenarioBucketRange},
		{"穿透至后续规则非默认", scenarioRolloutFallthroughToNextRule},
		{"穿透至默认且桶非空", scenarioRolloutFallthroughToDefault},
		{"注册类型校验-默认值", scenarioValidateDefault},
		{"注册类型校验-规则值", scenarioValidateRuleValue},
		{"注册拒绝未知操作符", scenarioValidateUnknownOp},
		{"注册拒绝百分比越界", scenarioValidatePctRange},
		{"注册拒绝非法条件值", scenarioValidateCondValue},
		{"拒绝注册不落库", scenarioRejectNotPersisted},
		{"注册覆盖整体替换", scenarioReplaceExisting},
		{"查询不存在", scenarioGetMissing},
		{"列举按键排序", scenarioListSorted},
		{"删除不存在", scenarioDeleteMissing},
		{"删除清除计数", scenarioDeleteClearsCounters},
		{"求值计数与命中计数", scenarioCounts},
		{"不存在开关求值不计次", scenarioEvaluateMissingNotCounted},
		{"统计快照结构", scenarioStatsShape},
		{"并发安全", scenarioConcurrent},
	}
	for _, sc := range scenarios {
		if err := sc.fn(); err != nil {
			return fmt.Errorf("%s: %w", sc.name, err)
		}
	}
	return nil
}

// freshReg registers a flag and returns the registry.
func freshReg(f flagger.Flag) (*registry.Registry, error) {
	r := registry.New()
	if err := r.Register(f); err != nil {
		return nil, err
	}
	return r, nil
}

func scenarioDefault() error {
	r, err := freshReg(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false})
	if err != nil {
		return err
	}
	res, err := r.Evaluate("f", flagger.Context{Attributes: map[string]any{"x": 1}})
	if err != nil {
		return err
	}
	if res.Value != false {
		return fmt.Errorf("value = %v, want false", res.Value)
	}
	if res.Reason != "default" {
		return fmt.Errorf("reason = %q, want default", res.Reason)
	}
	if res.Matched {
		return errors.New("matched = true, want false")
	}
	if res.Bucket != nil {
		return fmt.Errorf("bucket = %v, want nil", *res.Bucket)
	}
	return nil
}

func scenarioUnconditionalRule() error {
	r, err := freshReg(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{{Value: true}}})
	if err != nil {
		return err
	}
	res, err := r.Evaluate("f", flagger.Context{Attributes: map[string]any{}})
	if err != nil {
		return err
	}
	if res.Value != true {
		return fmt.Errorf("value = %v, want true", res.Value)
	}
	if res.Reason != "rule-0" {
		return fmt.Errorf("reason = %q, want rule-0", res.Reason)
	}
	if !res.Matched {
		return errors.New("matched = false, want true")
	}
	if res.Bucket != nil {
		return fmt.Errorf("bucket = %v, want nil", *res.Bucket)
	}
	return nil
}

func scenarioConditionOps() error {
	f := flagger.Flag{Key: "f", Type: flagger.TypeNumber, Default: 0, Rules: []flagger.Rule{
		{Value: 1, Conditions: []flagger.Condition{{Attribute: "country", Op: flagger.OpEq, Value: "US"}}},
		{Value: 2, Conditions: []flagger.Condition{{Attribute: "country", Op: flagger.OpIn, Value: []any{"CA", "MX"}}}},
		{Value: 3, Conditions: []flagger.Condition{{Attribute: "v", Op: flagger.OpGte, Value: 2.0}}},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	cases := []struct {
		attrs map[string]any
		want  any
		reas  string
	}{
		{map[string]any{"country": "US"}, 1, "rule-0"},
		{map[string]any{"country": "CA"}, 2, "rule-1"},
		{map[string]any{"country": "GB", "v": 3}, 3, "rule-2"},
		{map[string]any{"country": "GB", "v": 1}, 0, "default"},
		{map[string]any{"country": 3}, 0, "default"}, // eq type mismatch
	}
	for i, c := range cases {
		res, err := r.Evaluate("f", flagger.Context{Attributes: c.attrs})
		if err != nil {
			return err
		}
		if res.Value != c.want {
			return fmt.Errorf("case %d: value = %v, want %v", i, res.Value, c.want)
		}
		if res.Reason != c.reas {
			return fmt.Errorf("case %d: reason = %q, want %q", i, res.Reason, c.reas)
		}
	}
	return nil
}

func scenarioMissingAttr() error {
	r, err := freshReg(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "missing", Op: flagger.OpEq, Value: "x"}}},
	}})
	if err != nil {
		return err
	}
	res, err := r.Evaluate("f", flagger.Context{Attributes: map[string]any{}})
	if err != nil {
		return err
	}
	if res.Matched {
		return errors.New("matched = true, want false for missing attr")
	}
	if res.Reason != "default" {
		return fmt.Errorf("reason = %q, want default", res.Reason)
	}
	return nil
}

func scenarioExists() error {
	r, err := freshReg(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "v", Op: flagger.OpExists}}},
	}})
	if err != nil {
		return err
	}
	res, err := r.Evaluate("f", flagger.Context{Attributes: map[string]any{"v": 1}})
	if err != nil {
		return err
	}
	if !res.Matched {
		return errors.New("expected match for exists with present attr")
	}
	res, err = r.Evaluate("f", flagger.Context{Attributes: map[string]any{}})
	if err != nil {
		return err
	}
	if res.Matched {
		return errors.New("expected no match for exists with missing attr")
	}
	return nil
}

func scenarioEqTypeMismatch() error {
	r, err := freshReg(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "s", Op: flagger.OpEq, Value: 5.0}}},
	}})
	if err != nil {
		return err
	}
	// "abc" vs 5.0 -> types differ -> not equal -> eq false.
	res, err := r.Evaluate("f", flagger.Context{Attributes: map[string]any{"s": "abc"}})
	if err != nil {
		return err
	}
	if res.Matched {
		return errors.New("eq with type mismatch should not match")
	}
	// 5.0 vs 5.0 -> equal -> eq true.
	res, err = r.Evaluate("f", flagger.Context{Attributes: map[string]any{"s": 5.0}})
	if err != nil {
		return err
	}
	if !res.Matched {
		return errors.New("eq with equal numeric values should match")
	}
	return nil
}

func scenarioCompareNonNumber() error {
	r, err := freshReg(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "s", Op: flagger.OpGt, Value: 1.0}}},
	}})
	if err != nil {
		return err
	}
	res, err := r.Evaluate("f", flagger.Context{Attributes: map[string]any{"s": "abc"}})
	if err != nil {
		return err
	}
	if res.Matched {
		return errors.New("gt on string attr should not match")
	}
	return nil
}

func scenarioFirstMatchWins() error {
	r, err := freshReg(flagger.Flag{Key: "f", Type: flagger.TypeNumber, Default: 0, Rules: []flagger.Rule{
		{Value: 1, Conditions: []flagger.Condition{{Attribute: "x", Op: flagger.OpGte, Value: 1.0}}},
		{Value: 2, Conditions: []flagger.Condition{{Attribute: "x", Op: flagger.OpGte, Value: 100.0}}},
	}})
	if err != nil {
		return err
	}
	res, err := r.Evaluate("f", flagger.Context{Attributes: map[string]any{"x": 200.0}})
	if err != nil {
		return err
	}
	if res.Value != 1 {
		return fmt.Errorf("value = %v, want 1 (first match wins)", res.Value)
	}
	if res.Reason != "rule-0" {
		return fmt.Errorf("reason = %q, want rule-0", res.Reason)
	}
	return nil
}

func scenarioRolloutSticky() error {
	f := flagger.Flag{Key: "sticky", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Rollout: &flagger.Rollout{Percentage: 50, BucketBy: "userId"}},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	ctx := flagger.Context{Attributes: map[string]any{"userId": "u-42"}}
	r1, err := r.Evaluate("sticky", ctx)
	if err != nil {
		return err
	}
	r2, err := r.Evaluate("sticky", ctx)
	if err != nil {
		return err
	}
	if r1.Value != r2.Value || r1.Reason != r2.Reason || r1.Matched != r2.Matched {
		return fmt.Errorf("non-deterministic: r1=%+v r2=%+v", r1, r2)
	}
	if r1.Bucket == nil || r2.Bucket == nil {
		return errors.New("bucket nil for rollout")
	}
	if *r1.Bucket != *r2.Bucket {
		return fmt.Errorf("bucket differs: %d vs %d", *r1.Bucket, *r2.Bucket)
	}
	return nil
}

func scenarioRolloutMapIndependent() error {
	f := flagger.Flag{Key: "mi", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Rollout: &flagger.Rollout{Percentage: 50, BucketBy: "userId"}},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	ctx1 := flagger.Context{Attributes: map[string]any{"userId": "u-9", "a": 1, "b": 2, "c": 3}}
	ctx2 := flagger.Context{Attributes: map[string]any{"c": 3, "b": 2, "a": 1, "userId": "u-9"}}
	b1, err := r.Evaluate("mi", ctx1)
	if err != nil {
		return err
	}
	b2, err := r.Evaluate("mi", ctx2)
	if err != nil {
		return err
	}
	if b1.Bucket == nil || b2.Bucket == nil {
		return errors.New("bucket nil")
	}
	if *b1.Bucket != *b2.Bucket {
		return fmt.Errorf("bucket depends on map order: %d vs %d", *b1.Bucket, *b2.Bucket)
	}
	return nil
}

func scenarioRolloutZeroFallThrough() error {
	f := flagger.Flag{Key: "z", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "country", Op: flagger.OpEq, Value: "US"}},
			Rollout: &flagger.Rollout{Percentage: 0, BucketBy: "userId"}},
		{Value: false, Conditions: nil},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	res, err := r.Evaluate("z", flagger.Context{Attributes: map[string]any{"country": "US", "userId": "u1"}})
	if err != nil {
		return err
	}
	if res.Value != false {
		return fmt.Errorf("value = %v, want false (0%% must fall through)", res.Value)
	}
	if res.Reason != "rule-1" {
		return fmt.Errorf("reason = %q, want rule-1", res.Reason)
	}
	if res.Bucket == nil {
		return errors.New("bucket nil, want the 0%% rollout bucket")
	}
	return nil
}

func scenarioRolloutHundred() error {
	f := flagger.Flag{Key: "h", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Rollout: &flagger.Rollout{Percentage: 100, BucketBy: "userId"}},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	for i := 0; i < 200; i++ {
		res, err := r.Evaluate("h", flagger.Context{Attributes: map[string]any{"userId": key(i)}})
		if err != nil {
			return err
		}
		if res.Value != true || res.Reason != "rule-0" {
			return fmt.Errorf("u-%d: 100%% must win, got %+v", i, res)
		}
	}
	return nil
}

func scenarioRolloutDistribution() error {
	f := flagger.Flag{Key: "d", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Rollout: &flagger.Rollout{Percentage: 50, BucketBy: "userId"}},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	hits := 0
	for i := 0; i < 1000; i++ {
		res, err := r.Evaluate("d", flagger.Context{Attributes: map[string]any{"userId": key(i)}})
		if err != nil {
			return err
		}
		if res.Matched {
			hits++
		}
	}
	if hits < 400 || hits > 600 {
		return fmt.Errorf("50%% over 1000 keys got %d hits, want 400..600", hits)
	}
	return nil
}

func scenarioAnonymousBucket() error {
	f := flagger.Flag{Key: "anon", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Rollout: &flagger.Rollout{Percentage: 50, BucketBy: "userId"}},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	a, err := r.Evaluate("anon", flagger.Context{Attributes: map[string]any{}})
	if err != nil {
		return err
	}
	b, err := r.Evaluate("anon", flagger.Context{Attributes: map[string]any{"other": "x"}})
	if err != nil {
		return err
	}
	if a.Bucket == nil || b.Bucket == nil {
		return errors.New("bucket nil for anonymous context")
	}
	if *a.Bucket != *b.Bucket {
		return fmt.Errorf("anonymous bucket not stable: %d vs %d", *a.Bucket, *b.Bucket)
	}
	return nil
}

func scenarioBucketRange() error {
	f := flagger.Flag{Key: "br", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Rollout: &flagger.Rollout{Percentage: 50, BucketBy: "userId"}},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	for i := 0; i < 500; i++ {
		res, err := r.Evaluate("br", flagger.Context{Attributes: map[string]any{"userId": key(i)}})
		if err != nil {
			return err
		}
		if res.Bucket == nil {
			return errors.New("bucket nil")
		}
		if *res.Bucket < 0 || *res.Bucket >= 10000 {
			return fmt.Errorf("bucket %d out of [0,10000)", *res.Bucket)
		}
	}
	return nil
}

func scenarioRolloutFallthroughToNextRule() error {
	// First rule: country=US, value true, 0% rollout (always misses).
	// Second rule: catch-all, value false.
	f := flagger.Flag{Key: "ft", Type: flagger.TypeBool, Default: true, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "country", Op: flagger.OpEq, Value: "US"}},
			Rollout: &flagger.Rollout{Percentage: 0, BucketBy: "userId"}},
		{Value: false, Conditions: nil},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	res, err := r.Evaluate("ft", flagger.Context{Attributes: map[string]any{"country": "US", "userId": "u1"}})
	if err != nil {
		return err
	}
	if res.Value != false {
		return fmt.Errorf("value = %v, want false", res.Value)
	}
	if res.Reason != "rule-1" {
		return fmt.Errorf("reason = %q, want rule-1", res.Reason)
	}
	if res.Bucket == nil {
		return errors.New("bucket nil, want the failed rollout bucket")
	}
	return nil
}

func scenarioRolloutFallthroughToDefault() error {
	// A single rule with a 0% rollout: must fall through to default, with the
	// bucket still reported.
	f := flagger.Flag{Key: "fd", Type: flagger.TypeBool, Default: true, Rules: []flagger.Rule{
		{Value: false, Rollout: &flagger.Rollout{Percentage: 0, BucketBy: "userId"}},
	}}
	r, err := freshReg(f)
	if err != nil {
		return err
	}
	res, err := r.Evaluate("fd", flagger.Context{Attributes: map[string]any{"userId": "u1"}})
	if err != nil {
		return err
	}
	if res.Value != true {
		return fmt.Errorf("value = %v, want true (default)", res.Value)
	}
	if res.Reason != "default" {
		return fmt.Errorf("reason = %q, want default", res.Reason)
	}
	if res.Matched {
		return errors.New("matched = true, want false")
	}
	if res.Bucket == nil {
		return errors.New("bucket nil, want the failed rollout bucket")
	}
	return nil
}

func scenarioValidateDefault() error {
	r := registry.New()
	if err := r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: "yes"}); !errors.Is(err, flagger.ErrDefaultType) {
		return fmt.Errorf("err = %v, want ErrDefaultType", err)
	}
	if _, ok := r.Get("f"); ok {
		return errors.New("rejected flag was persisted")
	}
	if s := r.Stats(); s.FlagCount != 0 {
		return fmt.Errorf("flagCount = %d, want 0", s.FlagCount)
	}
	return nil
}

func scenarioValidateRuleValue() error {
	r := registry.New()
	err := r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{{Value: "notbool"}}})
	if !errors.Is(err, flagger.ErrRuleValueType) {
		return fmt.Errorf("err = %v, want ErrRuleValueType", err)
	}
	if _, ok := r.Get("f"); ok {
		return errors.New("rejected flag was persisted")
	}
	return nil
}

func scenarioValidateUnknownOp() error {
	r := registry.New()
	err := r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "a", Op: "matches"}}},
	}})
	if !errors.Is(err, flagger.ErrUnknownOp) {
		return fmt.Errorf("err = %v, want ErrUnknownOp", err)
	}
	return nil
}

func scenarioValidatePctRange() error {
	r := registry.New()
	err := r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Rollout: &flagger.Rollout{Percentage: 101, BucketBy: "u"}},
	}})
	if !errors.Is(err, flagger.ErrPctOutOfRange) {
		return fmt.Errorf("err = %v, want ErrPctOutOfRange", err)
	}
	return nil
}

func scenarioValidateCondValue() error {
	r := registry.New()
	// eq with array value.
	err := r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "a", Op: flagger.OpEq, Value: []any{"x"}}}},
	}})
	if !errors.Is(err, flagger.ErrCondNotScalar) {
		return fmt.Errorf("eq-array err = %v, want ErrCondNotScalar", err)
	}
	// in with scalar value.
	err = r.Register(flagger.Flag{Key: "g", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "a", Op: flagger.OpIn, Value: "x"}}},
	}})
	if !errors.Is(err, flagger.ErrCondNotArray) {
		return fmt.Errorf("in-scalar err = %v, want ErrCondNotArray", err)
	}
	// gt with non-number value.
	err = r.Register(flagger.Flag{Key: "h", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{
		{Value: true, Conditions: []flagger.Condition{{Attribute: "a", Op: flagger.OpGt, Value: "x"}}},
	}})
	if !errors.Is(err, flagger.ErrCondNotNumber) {
		return fmt.Errorf("gt-nonnumber err = %v, want ErrCondNotNumber", err)
	}
	return nil
}

func scenarioRejectNotPersisted() error {
	r := registry.New()
	_ = r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: "yes"})
	// Register a valid flag with the same key — it should succeed because the
	// prior invalid registration never landed.
	err := r.Register(flagger.Flag{Key: "f", Type: flagger.TypeString, Default: "ok"})
	if err != nil {
		return fmt.Errorf("valid re-register failed: %v", err)
	}
	got, ok := r.Get("f")
	if !ok || got.Type != flagger.TypeString {
		return fmt.Errorf("valid flag not stored: %+v ok=%v", got, ok)
	}
	return nil
}

func scenarioReplaceExisting() error {
	r := registry.New()
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false})
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: true, Rules: []flagger.Rule{{Value: true}}})
	got, _ := r.Get("f")
	if got.Default != true || len(got.Rules) != 1 {
		return fmt.Errorf("replace did not take effect: %+v", got)
	}
	return nil
}

func scenarioGetMissing() error {
	r := registry.New()
	if _, ok := r.Get("ghost"); ok {
		return errors.New("ghost flag should be absent")
	}
	return nil
}

func scenarioListSorted() error {
	r := registry.New()
	r.Register(flagger.Flag{Key: "z", Type: flagger.TypeBool, Default: false})
	r.Register(flagger.Flag{Key: "a", Type: flagger.TypeBool, Default: false})
	r.Register(flagger.Flag{Key: "m", Type: flagger.TypeBool, Default: false})
	out := r.List()
	if len(out) != 3 {
		return fmt.Errorf("len = %d, want 3", len(out))
	}
	want := []string{"a", "m", "z"}
	for i, w := range want {
		if out[i].Key != w {
			return fmt.Errorf("out[%d] = %q, want %q", i, out[i].Key, w)
		}
	}
	return nil
}

func scenarioDeleteMissing() error {
	r := registry.New()
	if err := r.Delete("ghost"); !errors.Is(err, registry.ErrNotFound) {
		return fmt.Errorf("err = %v, want ErrNotFound", err)
	}
	return nil
}

func scenarioDeleteClearsCounters() error {
	r := registry.New()
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{{Value: true}}})
	r.Evaluate("f", flagger.Context{Attributes: map[string]any{}})
	r.Evaluate("f", flagger.Context{Attributes: map[string]any{}})
	r.Delete("f")
	s := r.Stats()
	if s.FlagCount != 0 {
		return fmt.Errorf("flagCount = %d, want 0", s.FlagCount)
	}
	for _, fs := range s.Flags {
		if fs.Key == "f" {
			return fmt.Errorf("deleted flag still in stats: %+v", fs)
		}
	}
	// Re-register the same key — counters start fresh.
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false})
	s = r.Stats()
	if len(s.Flags) != 1 || s.Flags[0].Evaluations != 0 || s.Flags[0].Matches != 0 {
		return fmt.Errorf("re-registered flag has stale counters: %+v", s.Flags)
	}
	return nil
}

func scenarioCounts() error {
	r := registry.New()
	r.Register(flagger.Flag{
		Key: "f", Type: flagger.TypeNumber, Default: 10,
		Rules: []flagger.Rule{{Value: 100, Conditions: []flagger.Condition{{Attribute: "v", Op: flagger.OpGte, Value: 2.0}}}},
	})
	r.Evaluate("f", flagger.Context{Attributes: map[string]any{"v": 3.0}})               // match
	r.Evaluate("f", flagger.Context{Attributes: map[string]any{"v": 1.0}})               // default
	r.Evaluate("f", flagger.Context{Attributes: map[string]any{"missing": "x"}})          // default
	s := r.Stats()
	if s.TotalEvaluations != 3 {
		return fmt.Errorf("total = %d, want 3", s.TotalEvaluations)
	}
	if len(s.Flags) != 1 || s.Flags[0].Evaluations != 3 || s.Flags[0].Matches != 1 {
		return fmt.Errorf("per-flag stats = %+v", s.Flags)
	}
	return nil
}

func scenarioEvaluateMissingNotCounted() error {
	r := registry.New()
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false})
	_, err := r.Evaluate("ghost", flagger.Context{Attributes: map[string]any{}})
	if !errors.Is(err, registry.ErrNotFound) {
		return fmt.Errorf("err = %v, want ErrNotFound", err)
	}
	s := r.Stats()
	if s.TotalEvaluations != 0 {
		return fmt.Errorf("missing-flag eval was counted: total = %d", s.TotalEvaluations)
	}
	return nil
}

func scenarioStatsShape() error {
	r := registry.New()
	r.Register(flagger.Flag{Key: "a", Type: flagger.TypeBool, Default: false})
	r.Register(flagger.Flag{Key: "b", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{{Value: true}}})
	r.Evaluate("a", flagger.Context{Attributes: map[string]any{}}) // default
	r.Evaluate("b", flagger.Context{Attributes: map[string]any{}}) // match
	r.Evaluate("b", flagger.Context{Attributes: map[string]any{}}) // match
	s := r.Stats()
	if s.FlagCount != 2 {
		return fmt.Errorf("flagCount = %d, want 2", s.FlagCount)
	}
	if s.TotalEvaluations != 3 {
		return fmt.Errorf("total = %d, want 3", s.TotalEvaluations)
	}
	if len(s.Flags) != 2 {
		return fmt.Errorf("len(flags) = %d, want 2", len(s.Flags))
	}
	// Sorted by key.
	if s.Flags[0].Key != "a" || s.Flags[1].Key != "b" {
		return fmt.Errorf("stats not sorted: %+v", s.Flags)
	}
	if s.Flags[0].Evaluations != 1 || s.Flags[0].Matches != 0 {
		return fmt.Errorf("a stats = %+v", s.Flags[0])
	}
	if s.Flags[1].Evaluations != 2 || s.Flags[1].Matches != 2 {
		return fmt.Errorf("b stats = %+v", s.Flags[1])
	}
	return nil
}

func scenarioConcurrent() error {
	r := registry.New()
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{{Value: true}}})
	const workers = 8
	const iters = 100
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				switch i % 4 {
				case 0:
					r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: i%2 == 0})
				case 1:
					r.Evaluate("f", flagger.Context{Attributes: map[string]any{}})
				case 2:
					r.Get("f")
				case 3:
					r.Stats()
				}
			}
		}(w)
	}
	wg.Wait()
	// No race detector trigger + counters consistent: total == workers*iters/4 evals
	// (every worker does iters/4 evals at case 1).
	s := r.Stats()
	wantEvals := int64(workers) * int64(iters) / 4
	if s.TotalEvaluations != wantEvals {
		return fmt.Errorf("total = %d, want %d", s.TotalEvaluations, wantEvals)
	}
	return nil
}

// key produces a deterministic distinct bucket key for index i.
func key(i int) string {
	return fmt.Sprintf("k-%d", i)
}
