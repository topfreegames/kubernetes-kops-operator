package utils

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// DefaultRegistryBackoff waits about 15s in total before giving up on a registry request.
var DefaultRegistryBackoff = wait.Backoff{
	Duration: time.Second,
	Factor:   2,
	Jitter:   0.1,
	Steps:    5,
}

// RegistryTransport retries registry requests that fail with a transient network error, 429 or 5xx,
// and records the ones that still failed. After the first recorded failure it stops retrying until Reset.
type RegistryTransport struct {
	Base    http.RoundTripper
	Backoff wait.Backoff

	mu       sync.Mutex
	failures []string
}

func (t *RegistryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	log := ctrllog.Log.WithName("registry")
	backoff := t.Backoff
	if t.Err() != nil {
		backoff.Steps = 1
	}
	for {
		resp, err := t.Base.RoundTrip(req)
		if !isRetryableRegistryResponse(resp, err) {
			return resp, err
		}

		if backoff.Steps <= 1 || (req.Body != nil && req.GetBody == nil) {
			t.recordFailure(req, resp, err)
			return resp, err
		}

		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		delay := backoff.Step()
		log.Info("retrying registry request", "method", req.Method, "url", req.URL.Redacted(), "status", statusOf(resp), "error", err, "delay", delay)

		select {
		case <-req.Context().Done():
			t.recordFailure(req, nil, req.Context().Err())
			return nil, req.Context().Err()
		case <-time.After(delay):
		}

		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				t.recordFailure(req, nil, err)
				return nil, err
			}
			req = req.Clone(req.Context())
			req.Body = body
		}
	}
}

// Reset forgets the failures recorded so far.
func (t *RegistryTransport) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failures = nil
}

// Err returns an error listing the registry requests that failed since the last Reset.
func (t *RegistryTransport) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.failures) == 0 {
		return nil
	}
	return fmt.Errorf("failed to resolve image digests, %d registry requests failed: %s", len(t.failures), strings.Join(slices.Compact(slices.Sorted(slices.Values(t.failures))), "; "))
}

func (t *RegistryTransport) recordFailure(req *http.Request, resp *http.Response, err error) {
	failure := fmt.Sprintf("%s %s: %s", req.Method, req.URL.Redacted(), statusOf(resp))
	if err != nil {
		failure = fmt.Sprintf("%s %s: %v", req.Method, req.URL.Redacted(), err)
	}
	ctrllog.Log.WithName("registry").Error(errors.New(failure), "registry request failed")

	t.mu.Lock()
	defer t.mu.Unlock()
	t.failures = append(t.failures, failure)
}

func isRetryableRegistryResponse(resp *http.Response, err error) bool {
	if err != nil {
		var dnsErr *net.DNSError
		var netErr net.Error
		return errors.As(err, &dnsErr) ||
			(errors.As(err, &netErr) && netErr.Timeout()) ||
			errors.Is(err, syscall.ECONNRESET) ||
			errors.Is(err, syscall.ECONNREFUSED) ||
			errors.Is(err, io.EOF) ||
			errors.Is(err, io.ErrUnexpectedEOF)
	}
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError
}

func statusOf(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	return resp.Status
}
