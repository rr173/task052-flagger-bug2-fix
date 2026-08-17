package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer wires a fresh registry to an HTTP handler for one test.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := newServer()
	return httptest.NewServer(srv.routes())
}

func postJSON(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func doDelete(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestHTTPRegisterAndGet(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	flag := map[string]any{
		"key": "f", "type": "bool", "default": false,
		"rules": []any{
			map[string]any{"value": true, "conditions": []any{}},
		},
	}
	code, body := postJSON(t, srv.URL+"/flags", flag)
	if code != http.StatusOK {
		t.Fatalf("register code = %d body=%v", code, body)
	}
	if body["key"] != "f" {
		t.Errorf("body = %+v", body)
	}

	code, body = getJSON(t, srv.URL+"/flags/f")
	if code != http.StatusOK {
		t.Fatalf("get code = %d", code)
	}
	if body["key"] != "f" {
		t.Errorf("body = %+v", body)
	}

	code, body = getJSON(t, srv.URL+"/flags/ghost")
	if code != http.StatusNotFound {
		t.Fatalf("ghost code = %d want 404", code)
	}
}

func TestHTTPRegisterValidationRejects(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// bool flag with a string default: must be rejected and not persisted.
	bad := map[string]any{"key": "f", "type": "bool", "default": "yes"}
	code, body := postJSON(t, srv.URL+"/flags", bad)
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d want 400 body=%v", code, body)
	}
	// Confirm it was not persisted.
	code, _ = getJSON(t, srv.URL+"/flags/f")
	if code != http.StatusNotFound {
		t.Fatalf("rejected flag persisted: code = %d want 404", code)
	}
}

func TestHTTPEvaluate(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	flag := map[string]any{
		"key": "checkout", "type": "number", "default": 10,
		"rules": []any{
			map[string]any{
				"value": 100,
				"conditions": []any{
					map[string]any{"attribute": "version", "op": "gte", "value": 2.0},
				},
			},
		},
	}
	if code, _ := postJSON(t, srv.URL+"/flags", flag); code != http.StatusOK {
		t.Fatalf("register failed")
	}

	// version=3 -> 100 (rule-0).
	code, body := postJSON(t, srv.URL+"/flags/checkout/evaluate",
		map[string]any{"attributes": map[string]any{"version": 3.0}})
	if code != http.StatusOK {
		t.Fatalf("eval code = %d body=%v", code, body)
	}
	// JSON number 100 decodes to float64(100).
	if v, ok := body["value"].(float64); !ok || v != 100 {
		t.Errorf("value = %v, want 100", body["value"])
	}
	if body["reason"] != "rule-0" {
		t.Errorf("reason = %v, want rule-0", body["reason"])
	}
	// matched is a JSON boolean: assert with == true, not a numeric helper.
	if body["matched"] != true {
		t.Errorf("matched = %v, want true", body["matched"])
	}
	if body["bucket"] != nil {
		t.Errorf("bucket = %v, want nil (no rollout)", body["bucket"])
	}

	// version=1 -> default 10.
	_, body = postJSON(t, srv.URL+"/flags/checkout/evaluate",
		map[string]any{"attributes": map[string]any{"version": 1.0}})
	if v, ok := body["value"].(float64); !ok || v != 10 {
		t.Errorf("value = %v, want 10", body["value"])
	}
	if body["reason"] != "default" {
		t.Errorf("reason = %v, want default", body["reason"])
	}
	if body["matched"] != false {
		t.Errorf("matched = %v, want false", body["matched"])
	}

	// missing flag.
	code, _ = postJSON(t, srv.URL+"/flags/ghost/evaluate",
		map[string]any{"attributes": map[string]any{}})
	if code != http.StatusNotFound {
		t.Fatalf("ghost eval code = %d want 404", code)
	}
}

func TestHTTPRolloutSticky(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	flag := map[string]any{
		"key": "roll", "type": "bool", "default": false,
		"rules": []any{
			map[string]any{
				"value": true,
				"rollout": map[string]any{"percentage": 50, "bucketBy": "userId"},
			},
		},
	}
	if code, _ := postJSON(t, srv.URL+"/flags", flag); code != http.StatusOK {
		t.Fatalf("register failed")
	}

	ctx := map[string]any{"attributes": map[string]any{"userId": "u-42"}}
	_, b1 := postJSON(t, srv.URL+"/flags/roll/evaluate", ctx)
	_, b2 := postJSON(t, srv.URL+"/flags/roll/evaluate", ctx)
	if b1["value"] != b2["value"] {
		t.Errorf("non-deterministic value: %v vs %v", b1["value"], b2["value"])
	}
	if b1["bucket"] != b2["bucket"] {
		t.Errorf("non-deterministic bucket: %v vs %v", b1["bucket"], b2["bucket"])
	}
	if b1["bucket"] == nil {
		t.Errorf("bucket nil for rollout")
	}
}

func TestHTTPListSorted(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	for _, k := range []string{"z", "a", "m"} {
		postJSON(t, srv.URL+"/flags", map[string]any{"key": k, "type": "bool", "default": false})
	}
	_, body := getJSON(t, srv.URL+"/flags")
	arr, ok := body["flags"].([]any)
	if !ok {
		t.Fatalf("flags not array: %v", body)
	}
	if len(arr) != 3 {
		t.Fatalf("len = %d", len(arr))
	}
	want := []string{"a", "m", "z"}
	for i, w := range want {
		m, _ := arr[i].(map[string]any)
		if m["key"] != w {
			t.Errorf("flags[%d] = %v, want %s", i, m["key"], w)
		}
	}
}

func TestHTTPDelete(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	postJSON(t, srv.URL+"/flags", map[string]any{"key": "f", "type": "bool", "default": false})
	code, _ := doDelete(t, srv.URL+"/flags/f")
	if code != http.StatusOK {
		t.Fatalf("delete code = %d", code)
	}
	code, _ = doDelete(t, srv.URL+"/flags/f")
	if code != http.StatusNotFound {
		t.Fatalf("second delete code = %d want 404", code)
	}
}

func TestHTTPStats(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	postJSON(t, srv.URL+"/flags", map[string]any{"key": "f", "type": "bool", "default": false,
		"rules": []any{map[string]any{"value": true}}})
	postJSON(t, srv.URL+"/flags/f/evaluate", map[string]any{"attributes": map[string]any{}})
	postJSON(t, srv.URL+"/flags/f/evaluate", map[string]any{"attributes": map[string]any{}})
	code, body := getJSON(t, srv.URL+"/stats")
	if code != http.StatusOK {
		t.Fatalf("stats code = %d", code)
	}
	if v, ok := body["flagCount"].(float64); !ok || v != 1 {
		t.Errorf("flagCount = %v, want 1", body["flagCount"])
	}
	if v, ok := body["totalEvaluations"].(float64); !ok || v != 2 {
		t.Errorf("totalEvaluations = %v, want 2", body["totalEvaluations"])
	}
}
