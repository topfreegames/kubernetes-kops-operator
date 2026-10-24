package utils

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/util/wait"
)

func testRegistryTransport(steps int) *RegistryTransport {
	return &RegistryTransport{
		Base:    http.DefaultTransport,
		Backoff: wait.Backoff{Duration: time.Millisecond, Factor: 1, Steps: steps},
	}
}

func TestRegistryTransport(t *testing.T) {
	testCases := []struct {
		description     string
		statuses        []int
		steps           int
		expectedStatus  int
		expectedCalls   int32
		expectedFailure bool
	}{
		{
			description:    "should retry a 429 until the request succeeds",
			statuses:       []int{http.StatusTooManyRequests, http.StatusTooManyRequests, http.StatusOK},
			steps:          5,
			expectedStatus: http.StatusOK,
			expectedCalls:  3,
		},
		{
			description:    "should retry a 5xx until the request succeeds",
			statuses:       []int{http.StatusServiceUnavailable, http.StatusOK},
			steps:          5,
			expectedStatus: http.StatusOK,
			expectedCalls:  2,
		},
		{
			description:     "should record a request that still fails after every retry",
			statuses:        []int{http.StatusBadGateway},
			steps:           3,
			expectedStatus:  http.StatusBadGateway,
			expectedCalls:   3,
			expectedFailure: true,
		},
		{
			description:    "should not retry or record a 404",
			statuses:       []int{http.StatusNotFound},
			steps:          5,
			expectedStatus: http.StatusNotFound,
			expectedCalls:  1,
		},
		{
			description:    "should not retry or record a 401",
			statuses:       []int{http.StatusUnauthorized},
			steps:          5,
			expectedStatus: http.StatusUnauthorized,
			expectedCalls:  1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			g := NewWithT(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				w.WriteHeader(tc.statuses[min(n, len(tc.statuses))-1])
			}))
			defer server.Close()

			transport := testRegistryTransport(tc.steps)
			resp, err := (&http.Client{Transport: transport}).Get(server.URL + "/v2/repo/manifests/tag")
			g.Expect(err).NotTo(HaveOccurred())
			_ = resp.Body.Close()

			g.Expect(resp.StatusCode).To(Equal(tc.expectedStatus))
			g.Expect(calls.Load()).To(Equal(tc.expectedCalls))
			if tc.expectedFailure {
				g.Expect(transport.Err()).To(MatchError(ContainSubstring("/v2/repo/manifests/tag")))
			} else {
				g.Expect(transport.Err()).NotTo(HaveOccurred())
			}
		})
	}
}

func TestRegistryTransportNetworkError(t *testing.T) {
	g := NewWithT(t)
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	transport := testRegistryTransport(2)
	_, err := (&http.Client{Transport: transport}).Get(url + "/v2/")
	g.Expect(err).To(HaveOccurred())
	g.Expect(transport.Err()).To(HaveOccurred())

	transport.Reset()
	g.Expect(transport.Err()).NotTo(HaveOccurred())
}

func TestRegistryTransportRequestBody(t *testing.T) {
	g := NewWithT(t)
	var calls atomic.Int32
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	transport := testRegistryTransport(5)
	resp, err := (&http.Client{Transport: transport}).Post(server.URL+"/token", "text/plain", strings.NewReader("form"))
	g.Expect(err).NotTo(HaveOccurred())
	_ = resp.Body.Close()
	g.Expect(resp.StatusCode).To(Equal(http.StatusOK))
	mu.Lock()
	g.Expect(bodies).To(Equal([]string{"form", "form"}))
	mu.Unlock()

	calls.Store(0)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/token", io.NopCloser(bytes.NewBufferString("form")))
	g.Expect(err).NotTo(HaveOccurred())
	resp, err = transport.RoundTrip(req)
	g.Expect(err).NotTo(HaveOccurred())
	_ = resp.Body.Close()
	g.Expect(resp.StatusCode).To(Equal(http.StatusServiceUnavailable))
	g.Expect(calls.Load()).To(Equal(int32(1)))
	g.Expect(transport.Err()).To(HaveOccurred())
}

func TestRegistryTransportIsUsedByCraneDigest(t *testing.T) {
	g := NewWithT(t)
	var throttle atomic.Int32
	reg := registry.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") && throttle.Add(-1) >= 0 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		reg.ServeHTTP(w, r)
	}))
	defer server.Close()

	ref := strings.TrimPrefix(server.URL, "http://") + "/repo:tag"
	img, err := random.Image(1024, 1)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(crane.Push(img, ref)).To(Succeed())
	expected, err := img.Digest()
	g.Expect(err).NotTo(HaveOccurred())

	throttle.Store(2)
	_, err = crane.Digest(ref)
	g.Expect(err).To(HaveOccurred())

	transport := testRegistryTransport(5)
	transport.Base = remote.DefaultTransport
	defaultTransport := remote.DefaultTransport
	remote.DefaultTransport = transport
	defer func() { remote.DefaultTransport = defaultTransport }()

	throttle.Store(2)
	digest, err := crane.Digest(ref)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(digest).To(Equal(expected.String()))
	g.Expect(transport.Err()).NotTo(HaveOccurred())
}

func TestRegistryTransportStopsRetryingAfterAFailure(t *testing.T) {
	g := NewWithT(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	transport := testRegistryTransport(3)
	client := &http.Client{Transport: transport}
	get := func() int32 {
		calls.Store(0)
		resp, err := client.Get(server.URL + "/v2/")
		g.Expect(err).NotTo(HaveOccurred())
		_ = resp.Body.Close()
		return calls.Load()
	}

	g.Expect(get()).To(Equal(int32(3)))
	g.Expect(get()).To(Equal(int32(1)))

	transport.Reset()
	g.Expect(get()).To(Equal(int32(3)))
}
