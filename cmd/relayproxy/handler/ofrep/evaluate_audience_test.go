package ofrep_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffclient "github.com/thomaspoignant/go-feature-flag"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/config"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/handler/ofrep"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/metric"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/service"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/retriever/fileretriever"
)

const audienceFlagsYAML = `
aud-flag:
  variations: { on: true, off: false }
  defaultRule: { variation: off }
  targeting:
    - name: beta
      query: targetingKey in audience("beta")
      variation: on
plain-flag:
  variations: { on: true, off: false }
  defaultRule: { variation: on }
`

// singleClientManager serves one client whatever the API key.
type singleClientManager struct {
	service.FlagsetManager
	client *ffclient.GoFeatureFlag
}

func (m singleClientManager) FlagSet(string) (*ffclient.GoFeatureFlag, error) { return m.client, nil }

func (m singleClientManager) FlagSetName(string) (string, error) { return "default", nil }
func (m singleClientManager) Default() *ffclient.GoFeatureFlag   { return m.client }
func (m singleClientManager) IsDefaultFlagSet() bool             { return true }

func newAudienceCtrl(t *testing.T, fake *audiencetest.FakeProvider) ofrep.EvaluateCtrl {
	t.Helper()
	file := filepath.Join(t.TempDir(), "flags.yaml")
	require.NoError(t, os.WriteFile(file, []byte(audienceFlagsYAML), 0o600))
	var aud *ffclient.AudiencesConfig
	if fake != nil {
		aud = &ffclient.AudiencesConfig{Provider: fake}
	}
	client, err := ffclient.New(ffclient.Config{
		PollingInterval: time.Hour,
		Retriever:       &fileretriever.Retriever{Path: file},
		Audiences:       aud,
	})
	require.NoError(t, err)
	t.Cleanup(client.Close)
	metrics, err := metric.NewMetrics()
	require.NoError(t, err)
	return ofrep.NewOFREPEvaluate(singleClientManager{client: client}, metrics, config.OfrepEventStream{})
}

func doOFREP(t *testing.T, path string, handler func(*echo.Context) error, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(req, rec)
	require.NoError(t, handler(c))
	return rec
}

func TestEvaluate_Audiences(t *testing.T) {
	tests := []struct {
		name         string
		flagKey      string
		user         string
		setup        func(f *audiencetest.FakeProvider)
		noAudiences  bool
		wantValue    any
		wantStatus   string
		wantNoStore  bool
		wantHTTPCode int
	}{
		{
			name: "member", flagKey: "aud-flag", user: "bob", wantValue: true,
			wantStatus: flag.AudienceStatusHealthy, wantNoStore: true,
		},
		{
			name: "non member", flagKey: "aud-flag", user: "alice", wantValue: false,
			wantStatus: flag.AudienceStatusHealthy, wantNoStore: true,
		},
		{
			name: "store error", flagKey: "aud-flag", user: "bob", wantValue: false,
			setup:      func(f *audiencetest.FakeProvider) { f.FailAll(errors.New("boom")) },
			wantStatus: flag.AudienceStatusStoreError, wantNoStore: true,
		},
		{
			name: "audiences not configured", flagKey: "aud-flag", user: "bob", wantValue: false,
			noAudiences: true, wantStatus: flag.AudienceStatusNotConfigured, wantNoStore: true,
		},
		{name: "flag without audience", flagKey: "plain-flag", user: "bob", wantValue: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := audiencetest.NewFakeProvider()
			require.NoError(t, fake.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
			if tt.setup != nil {
				tt.setup(fake)
			}
			if tt.noAudiences {
				fake = nil
			}
			ctrl := newAudienceCtrl(t, fake)
			e := echo.New()
			e.POST("/ofrep/v1/evaluate/flags/:flagKey", ctrl.Evaluate)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/ofrep/v1/evaluate/flags/"+tt.flagKey,
				strings.NewReader(`{"context":{"targetingKey":"`+tt.user+`"}}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			e.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var resp map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantValue, resp["value"])
			md, _ := resp["metadata"].(map[string]any)
			if tt.wantStatus == "" {
				assert.NotContains(t, md, flag.AudienceStatusMetadataKey)
			} else {
				assert.Equal(t, tt.wantStatus, md[flag.AudienceStatusMetadataKey])
			}
			if tt.wantNoStore {
				assert.Equal(t, "no-store", rec.Header().Get(echo.HeaderCacheControl))
			} else {
				assert.Empty(t, rec.Header().Get(echo.HeaderCacheControl))
			}
		})
	}
}

func TestBulkEvaluate_Audiences(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantNoStore bool
		wantCalls   int
	}{
		{
			name: "all flags", body: `{"context":{"targetingKey":"bob"}}`,
			wantNoStore: true, wantCalls: 1,
		},
		{
			name:        "selected flags with audience",
			body:        `{"context":{"targetingKey":"bob","gofeatureflag":{"flagList":["aud-flag","plain-flag"]}}}`,
			wantNoStore: true, wantCalls: 1,
		},
		{
			name: "selected flags without audience",
			body: `{"context":{"targetingKey":"bob","gofeatureflag":{"flagList":["plain-flag"]}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := audiencetest.NewFakeProvider()
			require.NoError(t, fake.Add(context.Background(), "beta", audience.ListSettings{}, "bob"))
			ctrl := newAudienceCtrl(t, fake)
			rec := doOFREP(t, "/ofrep/v1/evaluate/flags", ctrl.BulkEvaluate, tt.body)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantCalls, fake.Calls())
			if tt.wantNoStore {
				assert.Equal(t, "no-store", rec.Header().Get(echo.HeaderCacheControl))
			} else {
				assert.Empty(t, rec.Header().Get(echo.HeaderCacheControl))
			}
		})
	}
}
