package registry

import (
	"errors"
	"testing"

	"task052-flagger/internal/flagger"
)

func TestRegisterAndGet(t *testing.T) {
	r := New()
	f := flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false}
	if err := r.Register(f); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, ok := r.Get("f")
	if !ok {
		t.Fatal("flag missing after register")
	}
	if got.Key != "f" {
		t.Errorf("got key %q", got.Key)
	}
	if _, ok := r.Get("ghost"); ok {
		t.Error("ghost flag should be absent")
	}
}

func TestRegisterValidation(t *testing.T) {
	r := New()
	bad := flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: "yes"}
	if err := r.Register(bad); !errors.Is(err, flagger.ErrDefaultType) {
		t.Fatalf("err = %v, want ErrDefaultType", err)
	}
	// Rejected flag must not be persisted.
	if _, ok := r.Get("f"); ok {
		t.Error("rejected flag was persisted")
	}
	if s := r.Stats(); s.FlagCount != 0 {
		t.Errorf("flagCount = %d, want 0", s.FlagCount)
	}
}

func TestRegisterReplacesExisting(t *testing.T) {
	r := New()
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false})
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: true, Rules: []flagger.Rule{{Value: true}}})
	got, _ := r.Get("f")
	if got.Default != true || len(got.Rules) != 1 {
		t.Errorf("replace did not take effect: %+v", got)
	}
}

func TestListSorted(t *testing.T) {
	r := New()
	r.Register(flagger.Flag{Key: "z", Type: flagger.TypeBool, Default: false})
	r.Register(flagger.Flag{Key: "a", Type: flagger.TypeBool, Default: false})
	r.Register(flagger.Flag{Key: "m", Type: flagger.TypeBool, Default: false})
	out := r.List()
	if len(out) != 3 {
		t.Fatalf("len = %d", len(out))
	}
	want := []string{"a", "m", "z"}
	for i, w := range want {
		if out[i].Key != w {
			t.Errorf("out[%d] = %q, want %q", i, out[i].Key, w)
		}
	}
}

func TestDelete(t *testing.T) {
	r := New()
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false})
	if err := r.Delete("f"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := r.Get("f"); ok {
		t.Error("flag still present after delete")
	}
	if err := r.Delete("f"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestEvaluateCountsAndMatch(t *testing.T) {
	r := New()
	r.Register(flagger.Flag{
		Key: "f", Type: flagger.TypeNumber, Default: 10,
		Rules: []flagger.Rule{{Value: 100, Conditions: []flagger.Condition{{Attribute: "v", Op: flagger.OpGte, Value: 2.0}}}},
	})
	ctx := flagger.Context{Attributes: map[string]any{"v": 3.0}}
	if _, err := r.Evaluate("ghost", ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ghost evaluate err = %v, want ErrNotFound", err)
	}
	r.Evaluate("f", ctx)              // match
	r.Evaluate("f", flagger.Context{Attributes: map[string]any{"v": 1.0}}) // default
	s := r.Stats()
	if s.TotalEvaluations != 2 {
		t.Errorf("total = %d, want 2", s.TotalEvaluations)
	}
	if len(s.Flags) != 1 || s.Flags[0].Evaluations != 2 || s.Flags[0].Matches != 1 {
		t.Errorf("per-flag stats = %+v", s.Flags)
	}
}

func TestDeleteClearsCounters(t *testing.T) {
	r := New()
	r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: false, Rules: []flagger.Rule{{Value: true}}})
	r.Evaluate("f", flagger.Context{Attributes: map[string]any{}})
	r.Delete("f")
	s := r.Stats()
	if s.FlagCount != 0 {
		t.Errorf("flagCount = %d, want 0", s.FlagCount)
	}
	for _, fs := range s.Flags {
		if fs.Key == "f" {
			t.Errorf("deleted flag still in stats: %+v", fs)
		}
	}
}

func TestConcurrentSafety(t *testing.T) {
	r := New()
	done := make(chan struct{})
	// Writer: re-register.
	go func() {
		for i := 0; i < 200; i++ {
			r.Register(flagger.Flag{Key: "f", Type: flagger.TypeBool, Default: i%2 == 0})
		}
		close(done)
	}()
	// Readers + evaluators.
	for i := 0; i < 200; i++ {
		r.Evaluate("f", flagger.Context{Attributes: map[string]any{}})
		r.Get("f")
		r.Stats()
	}
	<-done
}
