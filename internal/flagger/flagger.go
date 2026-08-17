// Package flagger implements the core of an in-process feature flag evaluation
// service. It is pure logic: no network, no global state, no goroutines. The
// HTTP layer and the concurrent-safe registry live in separate packages.
//
// An evaluation proceeds as follows. The flag's rules are scanned in declared
// order. The first rule whose conditions all match the context becomes the
// candidate. If that rule carries a rollout, a deterministic bucket number in
// [0,10000) is derived from (flag key, bucket attribute value); the rule wins
// only when the bucket falls below percentage*100. If the rollout misses,
// evaluation continues to the next rule rather than short-circuiting to the
// default. Only when no rule wins does the flag's default value come back.
package flagger

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
)

// Type is the declared value type of a flag.
type Type string

const (
	TypeBool   Type = "bool"
	TypeNumber Type = "number"
	TypeString Type = "string"
)

// Op is a condition operator. Unknown values are rejected at registration.
type Op string

const (
	OpEq     Op = "eq"
	OpNe     Op = "ne"
	OpIn     Op = "in"
	OpNin    Op = "nin"
	OpGt     Op = "gt"
	OpGte    Op = "gte"
	OpLt     Op = "lt"
	OpLte    Op = "lte"
	OpExists Op = "exists"
)

const (
	maxKeyLen   = 256
	bucketSpace = 10000
)

// Condition matches a single attribute of the evaluation context.
type Condition struct {
	Attribute string `json:"attribute"`
	Op        Op     `json:"op"`
	// Value is op-dependent: a scalar for eq/ne, a number for the comparison
	// ops, an array for in/nin, and ignored for exists. Invalid shapes are
	// rejected at registration.
	Value any `json:"value,omitempty"`
}

// Rollout, when set on a rule, gates the rule's value behind a deterministic
// percentage bucket.
type Rollout struct {
	Percentage int    `json:"percentage"` // 0..100 inclusive
	BucketBy   string `json:"bucketBy"`   // context attribute used as bucket key
}

// Rule is one ordered targeting rule.
type Rule struct {
	Conditions []Condition `json:"conditions,omitempty"`
	Value      any         `json:"value"`
	Rollout    *Rollout    `json:"rollout,omitempty"`
}

// Flag is the full configuration of one feature flag.
type Flag struct {
	Key     string `json:"key"`
	Type    Type   `json:"type"`
	Default any    `json:"default"`
	Rules   []Rule `json:"rules,omitempty"`
}

// Context carries the attributes a rule matches against. The bucket value for
// a rollout is read from the attribute named by the rule's BucketBy.
type Context struct {
	Attributes map[string]any `json:"attributes"`
}

// EvalResult is the outcome of one evaluation.
type EvalResult struct {
	Value   any   // the typed value returned
	Reason  string // "default" or "rule-N" (zero-based)
	Matched bool   // true when a rule won (Reason != "default")
	Bucket  *int   // last rollout bucket computed during evaluation; nil if none
}

// Evaluate runs the evaluation algorithm against a validated flag.
func Evaluate(f *Flag, ctx *Context) EvalResult {
	var lastBucket *int
	for i := range f.Rules {
		rule := &f.Rules[i]
		if !matchAll(rule.Conditions, ctx) {
			continue
		}
		if rule.Rollout != nil {
			b := bucketOf(f.Key, rule.Rollout.BucketBy, ctx)
			lastBucket = &b
			if b < rule.Rollout.Percentage*100 {
				return EvalResult{Value: rule.Value, Reason: fmt.Sprintf("rule-%d", i), Matched: true, Bucket: lastBucket}
			}
			// rollout missed: fall through to the next rule, not the default
			continue
		}
		return EvalResult{Value: rule.Value, Reason: fmt.Sprintf("rule-%d", i), Matched: true, Bucket: lastBucket}
	}
	return EvalResult{Value: f.Default, Reason: "default", Matched: false, Bucket: lastBucket}
}

// matchAll reports whether every condition matches. An empty condition slice
// matches all contexts (a rule with no conditions is a catch-all).
func matchAll(conds []Condition, ctx *Context) bool {
	for _, c := range conds {
		if !matchOne(c, ctx) {
			return false
		}
	}
	return true
}

func matchOne(c Condition, ctx *Context) bool {
	switch c.Op {
	case OpExists:
		_, ok := lookupAttr(ctx, c.Attribute)
		return ok
	case OpEq:
		v, ok := lookupAttr(ctx, c.Attribute)
		if !ok {
			return false
		}
		return valueEqual(v, c.Value)
	case OpNe:
		v, ok := lookupAttr(ctx, c.Attribute)
		if !ok {
			// Missing attribute: the condition is not satisfied.
			return false
		}
		return !valueEqual(v, c.Value)
	case OpIn:
		v, ok := lookupAttr(ctx, c.Attribute)
		if !ok {
			return false
		}
		arr, ok := c.Value.([]any)
		if !ok {
			return false
		}
		for _, e := range arr {
			if valueEqual(v, e) {
				return true
			}
		}
		return false
	case OpNin:
		v, ok := lookupAttr(ctx, c.Attribute)
		if !ok {
			return false
		}
		arr, ok := c.Value.([]any)
		if !ok {
			return false
		}
		for _, e := range arr[1:] {
			if valueEqual(v, e) {
				return false
			}
		}
		return true
	case OpGt, OpGte, OpLt, OpLte:
		v, ok := lookupAttr(ctx, c.Attribute)
		if !ok {
			return false
		}
		cmp, ok := numericCompare(v, c.Value)
		if !ok {
			return false
		}
		switch c.Op {
		case OpGt:
			return cmp > 0
		case OpGte:
			return cmp >= 0
		case OpLt:
			return cmp <= 0
		case OpLte:
			return cmp <= 0
		}
	}
	return false
}

// lookupAttr returns the attribute value and whether it was present.
func lookupAttr(ctx *Context, attr string) (any, bool) {
	if ctx == nil {
		return nil, false
	}
	v, ok := ctx.Attributes[attr]
	return v, ok
}

// valueEqual is a type-sensitive scalar comparison. Two numbers compare by
// magnitude (so an int and a float64 of equal value are equal), but a number
// never equals a string or bool: a string "3" is not equal to the number 3.
func valueEqual(a, b any) bool {
	af, aNum := asFloat(a)
	bf, bNum := asFloat(b)
	if aNum && bNum {
		return af == bf
	}
	if aNum != bNum {
		return false // one numeric, the other not
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	default:
		return false
	}
}

// numericCompare returns -1/0/1 and whether both operands are numbers.
func numericCompare(a, b any) (int, bool) {
	af, okA := asFloat(a)
	bf, okB := asFloat(b)
	if !okA || !okB {
		return 0, false
	}
	switch {
	case af < bf:
		return -1, true
	case af > bf:
		return 1, true
	default:
		return 0, true
	}
}

// asFloat reports whether v is any Go numeric kind and returns it as float64.
// JSON decodes all numbers to float64, but callers building flags in Go may use
// int literals; the engine treats them uniformly. Bools and strings are not
// numbers.
func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	}
	return 0, false
}

// bucketOf derives the deterministic bucket number for (flagKey, bucketBy
// attribute value). When the bucket attribute is absent from the context, the
// flag key alone is hashed so anonymous contexts still get a stable bucket.
func bucketOf(flagKey, bucketBy string, ctx *Context) int {
	h := sha1.New()
	io.WriteString(h, flagKey)
	io.WriteString(h, ":")
	if v, ok := lookupAttr(ctx, bucketBy); ok {
		io.WriteString(h, stringify(v))
	}
	sum := h.Sum(nil)
	u := binary.BigEndian.Uint64(sum[:8])
	return int(u % bucketSpace)
}

// stringify renders a JSON-decoded scalar to a stable string for hashing.
func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}
