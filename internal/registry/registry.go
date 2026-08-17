// Package registry holds the concurrent-safe in-memory store of feature flags
// and the evaluation counters backing the /stats endpoint.
package registry

import (
	"errors"
	"sort"
	"sync"

	"task052-flagger/internal/flagger"
)

// ErrNotFound is returned when a flag key is absent.
var ErrNotFound = errors.New("flag not found")

// Registry is safe for concurrent use. All access goes through its methods.
type Registry struct {
	mu      sync.RWMutex
	flags   map[string]flagger.Flag
	evals   map[string]int64 // per-flag successful evaluations
	matches map[string]int64 // per-flag rule hits (value != default)
	total   int64           // global successful evaluations
}

// New constructs an empty registry.
func New() *Registry {
	return &Registry{
		flags:   make(map[string]flagger.Flag),
		evals:   make(map[string]int64),
		matches: make(map[string]int64),
	}
}

// Register validates and stores a flag, replacing any existing flag with the
// same key. It does not reset evaluation counters: re-registering a key counts
// as the same logical flag for accounting purposes.
func (r *Registry) Register(f flagger.Flag) error {
	if err := flagger.Validate(&f); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flags[f.Key] = f
	return nil
}

// Get returns the flag for a key. The boolean is false when absent.
func (r *Registry) Get(key string) (flagger.Flag, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.flags[key]
	return f, ok
}

// List returns all flags sorted by key.
func (r *Registry) List() []flagger.Flag {
	r.mu.RLock()
	keys := make([]string, 0, len(r.flags))
	for k := range r.flags {
		keys = append(keys, k)
	}
	r.mu.RUnlock()
	sort.Strings(keys)
	var out []flagger.Flag
	for _, k := range keys {
		out = append(out, r.flags[k])
	}
	return out
}

// Delete removes a flag and its counters. Missing key -> ErrNotFound.
func (r *Registry) Delete(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.flags[key]; !ok {
		return ErrNotFound
	}
	delete(r.flags, key)
	delete(r.evals, key)
	delete(r.matches, key)
	return nil
}

// Evaluate evaluates a flag against a context, recording counters. It returns
// ErrNotFound when the flag is absent (and does not count such a call).
func (r *Registry) Evaluate(key string, ctx flagger.Context) (flagger.EvalResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.flags[key]
	if !ok {
		return flagger.EvalResult{}, ErrNotFound
	}
	res := flagger.Evaluate(&f, &ctx)
	r.evals[key]++
	r.total++
	if res.Matched {
		r.matches[key]++
	}
	return res, nil
}

// FlagStat is the per-flag accounting in a Stats snapshot.
type FlagStat struct {
	Key         string `json:"key"`
	Evaluations int64  `json:"evaluations"`
	Matches     int64  `json:"matches"`
}

// Stats is the summary returned by the /stats endpoint.
type Stats struct {
	FlagCount        int64      `json:"flagCount"`
	TotalEvaluations int64      `json:"totalEvaluations"`
	Flags            []FlagStat `json:"flags"`
}

// Stats returns a point-in-time snapshot of the counters.
func (r *Registry) Stats() Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.flags))
	for k := range r.flags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	stats := Stats{FlagCount: int64(len(r.flags)), TotalEvaluations: r.total}
	for _, k := range keys {
		stats.Flags = append(stats.Flags, FlagStat{Key: k, Evaluations: r.evals[k], Matches: r.matches[k]})
	}
	return stats
}
