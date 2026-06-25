package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// readTrafficLog parses a JSONL traffic log into records.
func readTrafficLog(t *testing.T, path string) []trafficRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read traffic log: %v", err)
	}
	var recs []trafficRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec trafficRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func TestLoggingRoundTripperRecordsRequestAndResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"size":"m8gd.large"}` {
			t.Errorf("server saw request body %q, want it preserved", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"pgxxx","state":"creating"}`))
	}))
	defer srv.Close()

	logPath := filepath.Join(t.TempDir(), "traffic.jsonl")
	client := &http.Client{Transport: newLoggingRoundTripper(http.DefaultTransport, logPath)}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/project/pjx/location/aws-us-east-1/postgres/tf-acc-1?foo=bar", bytes.NewReader([]byte(`{"size":"m8gd.large"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer pat-secret-should-not-be-logged")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// The caller must still see an intact, readable response body.
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(respBody) != `{"id":"pgxxx","state":"creating"}` {
		t.Errorf("caller saw response body %q, want it preserved", respBody)
	}

	recs := readTrafficLog(t, logPath)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", rec.Method)
	}
	if rec.Path != "/project/pjx/location/aws-us-east-1/postgres/tf-acc-1" {
		t.Errorf("path = %q, want the URL path without query", rec.Path)
	}
	if rec.Query != "foo=bar" {
		t.Errorf("query = %q, want foo=bar", rec.Query)
	}
	if rec.RequestBody != `{"size":"m8gd.large"}` {
		t.Errorf("request_body = %q", rec.RequestBody)
	}
	if rec.Status != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Status)
	}
	if rec.ResponseBody != `{"id":"pgxxx","state":"creating"}` {
		t.Errorf("response_body = %q", rec.ResponseBody)
	}
	if rec.RequestHeaders["Content-Type"] != "application/json" {
		t.Errorf("request Content-Type not captured: %v", rec.RequestHeaders)
	}
	if rec.ResponseHeaders["Content-Type"] != "application/json" {
		t.Errorf("response Content-Type not captured: %v", rec.ResponseHeaders)
	}
}

// The bearer token must never be written to the traffic log.
func TestLoggingRoundTripperRedactsAuthorization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	logPath := filepath.Join(t.TempDir(), "traffic.jsonl")
	client := &http.Client{Transport: newLoggingRoundTripper(http.DefaultTransport, logPath)}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/project", nil)
	req.Header.Set("Authorization", "Bearer pat-supersecret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "supersecret") || strings.Contains(string(data), "Authorization") {
		t.Fatalf("traffic log leaked authorization material:\n%s", data)
	}
}

// Two independent loggers pointed at the same file (parallel Terraform / aliased
// providers) must not interleave a JSONL line.
func TestLoggingRoundTripperConcurrentSameFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	logPath := filepath.Join(t.TempDir(), "traffic.jsonl")
	// Two separate logger instances (separate per-instance state) on one path.
	clientA := &http.Client{Transport: newLoggingRoundTripper(http.DefaultTransport, logPath)}
	clientB := &http.Client{Transport: newLoggingRoundTripper(http.DefaultTransport, logPath)}

	const perClient = 50
	var wg sync.WaitGroup
	for _, c := range []*http.Client{clientA, clientB} {
		for i := 0; i < perClient; i++ {
			wg.Add(1)
			go func(c *http.Client) {
				defer wg.Done()
				req, _ := http.NewRequest(http.MethodPost, srv.URL+"/project", bytes.NewReader([]byte(`{"name":"x"}`)))
				req.Header.Set("Content-Type", "application/json")
				resp, err := c.Do(req)
				if err == nil {
					_, _ = io.ReadAll(resp.Body)
					resp.Body.Close()
				}
			}(c)
		}
	}
	wg.Wait()

	recs := readTrafficLog(t, logPath)
	if len(recs) != 2*perClient {
		t.Fatalf("got %d records, want %d (a torn line means concurrent corruption)", len(recs), 2*perClient)
	}
}

// When UBICLOUD_TRAFFIC_LOG is unset the provider must not install a logging
// client (production must be inert).
func TestNewTrafficLoggingHTTPClientInertWithoutEnv(t *testing.T) {
	t.Setenv("UBICLOUD_TRAFFIC_LOG", "")
	if hc := newTrafficLoggingHTTPClient(); hc != nil {
		t.Fatalf("expected nil client when env unset, got %#v", hc)
	}

	logPath := filepath.Join(t.TempDir(), "traffic.jsonl")
	t.Setenv("UBICLOUD_TRAFFIC_LOG", logPath)
	if hc := newTrafficLoggingHTTPClient(); hc == nil {
		t.Fatal("expected non-nil client when env set")
	}
}
