// Copyright (c) The OpenBao Contributors
// SPDX-License-Identifier: MPL-2.0

package http

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/openbao/openbao/audit"
	"github.com/openbao/openbao/helper/namespace"
	"github.com/openbao/openbao/sdk/v2/helper/salt"
	"github.com/openbao/openbao/sdk/v2/logical"
	"github.com/stretchr/testify/require"
)

func TestAuditHttp_Integration(t *testing.T) {
	var lock sync.Mutex
	var badRequests int
	logs := []map[string]interface{}{}
	logRoute := "/audit"

	requiredHeaders := http.Header{}
	requiredHeaders.Add("X-Gitlab-Openbao-Token", "foobarfud")

	testServer := httptest.NewServer(GetTestAuditHandler(t, &lock, &logs, logRoute, requiredHeaders, &badRequests))
	defer testServer.Close()

	url := testServer.URL + logRoute

	backend, err := Factory(t.Context(), &audit.BackendConfig{
		SaltConfig: &salt.Config{},
		SaltView:   &logical.InmemStorage{},
		Config: map[string]string{
			"uri":     url,
			"headers": `{"Content-Type":["application/json"],"Accept":["application/json"],"X-Gitlab-Openbao-Token":["foobarfud"]}`,
		},
	})

	// We expect all test cases to be rejected.
	require.NoError(t, err)

	in := &logical.LogInput{
		Auth: &logical.Auth{
			ClientToken:     "foo",
			Accessor:        "bar",
			EntityID:        "foobarentity",
			DisplayName:     "testtoken",
			NoDefaultPolicy: true,
			Policies:        []string{"root"},
			TokenType:       logical.TokenTypeService,
		},
		Request: &logical.Request{
			Operation: logical.UpdateOperation,
			Path:      "/foo",
			Connection: &logical.Connection{
				RemoteAddr: "127.0.0.1",
			},
			WrapInfo: &logical.RequestWrapInfo{
				TTL: 60 * time.Second,
			},
			Headers: map[string][]string{
				"foo": {"bar"},
			},
		},
	}

	ctx := namespace.RootContext(t.Context())
	err = backend.LogRequest(ctx, in)
	require.NoError(t, err)

	require.Equal(t, badRequests, 0)

	lock.Lock()
	defer lock.Unlock()

	require.Equal(t, 1, len(logs))
	require.Contains(t, logs[0], "request")

	request := logs[0]["request"].(map[string]interface{})
	require.Contains(t, request, "path")
	require.Equal(t, request["path"].(string), "/foo")
}

func TestAuditHttp_RequestTimeoutConfig(t *testing.T) {
	t.Run("defaults to no timeout when unset", func(t *testing.T) {
		b, err := timeoutTestBackend(t, nil)
		require.NoError(t, err)
		require.Equal(t, time.Duration(0), b.(*Backend).requestTimeout)
	})

	t.Run("honors a configured value", func(t *testing.T) {
		b, err := timeoutTestBackend(t, map[string]string{"request_timeout": "5s"})
		require.NoError(t, err)
		require.Equal(t, 5*time.Second, b.(*Backend).requestTimeout)
	})

	t.Run("rejects an invalid value", func(t *testing.T) {
		_, err := timeoutTestBackend(t, map[string]string{"request_timeout": "not-a-duration"})
		require.Error(t, err)
	})

	t.Run("treats 0 as no timeout", func(t *testing.T) {
		b, err := timeoutTestBackend(t, map[string]string{"request_timeout": "0"})
		require.NoError(t, err)
		require.Equal(t, time.Duration(0), b.(*Backend).requestTimeout)
	})
}

func TestAuditHttp_RequestTimeoutFires(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer testServer.Close()

	backend, err := Factory(context.Background(), &audit.BackendConfig{
		SaltConfig: &salt.Config{},
		SaltView:   &logical.InmemStorage{},
		Config: map[string]string{
			"uri":             testServer.URL,
			"request_timeout": "1s",
		},
	})
	require.NoError(t, err)

	ctx := namespace.RootContext(context.Background())
	start := time.Now()
	err = backend.LogRequest(ctx, timeoutTestLogInput())
	elapsed := time.Since(start)

	// The audit POST is abandoned at request_timeout instead of hanging for the
	// full server delay.
	require.Error(t, err)
	require.Less(t, elapsed, 3*time.Second)
}

func TestAuditHttp_NoTimeoutByDefault(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer testServer.Close()

	// No request_timeout configured -> no device-level deadline, so a slow but
	// completing request still succeeds (preserves the pre-patch behaviour).
	backend, err := Factory(context.Background(), &audit.BackendConfig{
		SaltConfig: &salt.Config{},
		SaltView:   &logical.InmemStorage{},
		Config:     map[string]string{"uri": testServer.URL},
	})
	require.NoError(t, err)

	ctx := namespace.RootContext(context.Background())
	require.NoError(t, backend.LogRequest(ctx, timeoutTestLogInput()))
}

func timeoutTestLogInput() *logical.LogInput {
	return &logical.LogInput{
		Auth: &logical.Auth{
			ClientToken:     "foo",
			Accessor:        "bar",
			EntityID:        "foobarentity",
			DisplayName:     "testtoken",
			NoDefaultPolicy: true,
			Policies:        []string{"root"},
			TokenType:       logical.TokenTypeService,
		},
		Request: &logical.Request{
			Operation: logical.UpdateOperation,
			Path:      "/foo",
			Connection: &logical.Connection{
				RemoteAddr: "127.0.0.1",
			},
		},
	}
}

func timeoutTestBackend(t *testing.T, config map[string]string) (audit.Backend, error) {
	t.Helper()

	cfg := map[string]string{"uri": "http://127.0.0.1:1"}
	for k, v := range config {
		cfg[k] = v
	}

	return Factory(context.Background(), &audit.BackendConfig{
		SaltConfig: &salt.Config{},
		SaltView:   &logical.InmemStorage{},
		Config:     cfg,
	})
}
