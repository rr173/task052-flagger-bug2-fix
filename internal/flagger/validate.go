package flagger

import "fmt"

// Validation errors. Callers may use errors.Is to distinguish them.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrEmptyKey        Error = "flag key must be non-empty"
	ErrKeyTooLong      Error = "flag key exceeds 256 bytes"
	ErrUnknownType     Error = "unknown flag type"
	ErrDefaultType     Error = "default value does not match declared type"
	ErrRuleValueType   Error = "rule value does not match declared type"
	ErrEmptyAttr       Error = "condition attribute must be non-empty"
	ErrAttrTooLong     Error = "condition attribute exceeds 256 bytes"
	ErrUnknownOp       Error = "unknown operator"
	ErrCondNotScalar   Error = "eq/ne value must be a scalar"
	ErrCondNotArray    Error = "in/nin value must be an array"
	ErrCondNotNumber   Error = "comparison value must be a number"
	ErrPctOutOfRange   Error = "rollout percentage must be in [0,100]"
	ErrEmptyBucketBy   Error = "rollout bucketBy must be non-empty"
	ErrBucketByTooLong Error = "rollout bucketBy exceeds 256 bytes"
)

// Validate checks that a flag is well-formed. A flag that fails validation
// must not be persisted: callers should reject the whole configuration rather
// than partially applying it.
func Validate(f *Flag) error {
	if f.Key == "" {
		return ErrEmptyKey
	}
	switch f.Type {
	case TypeBool, TypeNumber, TypeString:
	default:
		return ErrUnknownType
	}
	if !valueMatchesType(f.Default, f.Type) {
		return ErrDefaultType
	}
	for i := range f.Rules {
		r := &f.Rules[i]
		if !valueMatchesType(r.Value, f.Type) {
			return fmt.Errorf("rule %d: %w", i, ErrRuleValueType)
		}
		for j, c := range r.Conditions {
			if err := validateCondition(c); err != nil {
				return fmt.Errorf("rule %d condition %d: %w", i, j, err)
			}
		}
		if r.Rollout != nil {
			if r.Rollout.Percentage < 0 || r.Rollout.Percentage > 100 {
				return fmt.Errorf("rule %d: %w", i, ErrPctOutOfRange)
			}
			if r.Rollout.BucketBy == "" {
				return fmt.Errorf("rule %d: %w", i, ErrEmptyBucketBy)
			}
			if len(r.Rollout.BucketBy) > maxKeyLen {
				return fmt.Errorf("rule %d: %w", i, ErrBucketByTooLong)
			}
		}
	}
	return nil
}

func validateCondition(c Condition) error {
	if c.Attribute == "" {
		return ErrEmptyAttr
	}
	if len(c.Attribute) > maxKeyLen {
		return ErrAttrTooLong
	}
	switch c.Op {
	case OpExists:
		// value ignored
	case OpEq, OpNe:
		if !isScalar(c.Value) {
			return ErrCondNotScalar
		}
	case OpIn, OpNin:
		if _, ok := c.Value.([]any); !ok {
			return ErrCondNotArray
		}
	case OpGt, OpGte, OpLt, OpLte:
		if _, ok := asFloat(c.Value); !ok {
			return ErrCondNotNumber
		}
	default:
		return ErrUnknownOp
	}
	return nil
}

// valueMatchesType reports whether a value conforms to a declared flag type.
// Numbers accept any numeric kind; JSON decodes all numbers to float64, but
// Go callers may pass int literals.
func valueMatchesType(v any, t Type) bool {
	switch t {
	case TypeBool:
		_, ok := v.(bool)
		return ok
	case TypeNumber:
		_, ok := asFloat(v)
		return ok
	case TypeString:
		_, ok := v.(string)
		return ok
	}
	return false
}

// isScalar reports whether v is a string, bool, or number — the operands an
// eq/ne condition may compare against.
func isScalar(v any) bool {
	switch v.(type) {
	case string, bool:
		return true
	}
	if _, ok := asFloat(v); ok {
		return true
	}
	return false
}
