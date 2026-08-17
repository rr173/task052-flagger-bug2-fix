package flagger

import "testing"

// TestProbeNinChecksFirstElement asserts that the nin (not-in) operator
// considers every element of its value array, including the first. Skipping
// the first element would let a value that equals arr[0] be wrongly judged as
// "not in" the array, matching the rule when it should not.
func TestProbeNinChecksFirstElement(t *testing.T) {
	f := &Flag{Key: "f", Type: TypeBool, Default: false, Rules: []Rule{
		{Value: true, Conditions: []Condition{
			{Attribute: "country", Op: OpNin, Value: []any{"CA", "MX"}},
		}},
	}}
	// country=CA is in [CA, MX], so nin is false -> rule must NOT match -> default false.
	got := Evaluate(f, &Context{Attributes: map[string]any{"country": "CA"}})
	if got.Matched {
		t.Fatalf("nin skipped first element: country=CA is in [CA,MX], rule must not match, got %+v", got)
	}
	if got.Value != false {
		t.Fatalf("value = %v, want false (default)", got.Value)
	}
	if got.Reason != "default" {
		t.Fatalf("reason = %q, want default", got.Reason)
	}
}
