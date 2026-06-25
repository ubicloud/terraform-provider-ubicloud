package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync"
)

// trafficRecord is one captured request/response pair, written as a JSONL line
// to the file named by UBICLOUD_TRAFFIC_LOG. It is consumed by the differential
// harness (scripts/) to validate provider traffic against openapi.yml. The
// Authorization header is deliberately never recorded.
type trafficRecord struct {
	Method          string            `json:"method"`
	Path            string            `json:"path"`
	Query           string            `json:"query,omitempty"`
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	RequestBody     string            `json:"request_body"`
	Status          int               `json:"status"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody    string            `json:"response_body"`
	Error           string            `json:"error,omitempty"`
}

// loggingRoundTripper wraps an http.RoundTripper and appends a trafficRecord for
// every round trip. Bodies are buffered and restored so the wrapped transport
// and the caller still see intact streams.
type loggingRoundTripper struct {
	wrapped http.RoundTripper
	path    string
}

func newLoggingRoundTripper(wrapped http.RoundTripper, path string) *loggingRoundTripper {
	return &loggingRoundTripper{wrapped: wrapped, path: path}
}

// Append serialization is keyed by file path, not by logger instance, so that
// multiple provider instances/aliases pointed at the same UBICLOUD_TRAFFIC_LOG
// (Terraform runs resources in parallel) never interleave a JSONL line.
var (
	logLocksMu sync.Mutex
	logLocks   = map[string]*sync.Mutex{}
)

func lockForPath(path string) *sync.Mutex {
	logLocksMu.Lock()
	defer logLocksMu.Unlock()
	mu, ok := logLocks[path]
	if !ok {
		mu = &sync.Mutex{}
		logLocks[path] = mu
	}
	return mu
}

// loggedHeaders is the allowlist of headers copied into the traffic log. It
// excludes Authorization so the bearer token never lands on disk.
var loggedHeaders = []string{"Content-Type", "Accept"}

func headerSubset(h http.Header) map[string]string {
	out := map[string]string{}
	for _, k := range loggedHeaders {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (l *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := trafficRecord{
		Method:         req.Method,
		Path:           req.URL.Path,
		Query:          req.URL.RawQuery,
		RequestHeaders: headerSubset(req.Header),
	}
	if req.Body != nil {
		reqBody, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err == nil {
			rec.RequestBody = string(reqBody)
			req.Body = io.NopCloser(bytes.NewReader(reqBody))
		}
	}

	resp, err := l.wrapped.RoundTrip(req)
	if err != nil {
		rec.Error = err.Error()
		l.write(rec)
		return resp, err
	}

	if resp.Body != nil {
		respBody, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr == nil {
			rec.ResponseBody = string(respBody)
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
		} else {
			rec.Error = rerr.Error()
		}
	}
	rec.Status = resp.StatusCode
	rec.ResponseHeaders = headerSubset(resp.Header)
	l.write(rec)
	return resp, nil
}

func (l *loggingRoundTripper) write(rec trafficRecord) {
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	mu := lockForPath(l.path)
	mu.Lock()
	defer mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// newTrafficLoggingHTTPClient returns an *http.Client that records traffic to
// UBICLOUD_TRAFFIC_LOG, or nil when the variable is unset so production builds
// are inert and use the client's default transport.
func newTrafficLoggingHTTPClient() *http.Client {
	path := os.Getenv("UBICLOUD_TRAFFIC_LOG")
	if path == "" {
		return nil
	}
	return &http.Client{Transport: newLoggingRoundTripper(http.DefaultTransport, path)}
}
