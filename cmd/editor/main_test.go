package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/modules/core/model"
)

const audienceFlag = `{
  "variations": {"A": false, "B": true},
  "targeting": [{"name": "beta", "query": "targetingKey in audience(\"beta\")", "variation": "B"}],
  "defaultRule": {"variation": "A"}
}`

func TestEvaluateHandlerAudiences(t *testing.T) {
	tests := []struct {
		name       string
		membership string
		wantValue  any
		wantStatus string
	}{
		{name: "no simulation: not configured", membership: "", wantValue: false, wantStatus: "NOT_CONFIGURED"},
		{
			name: "simulated member", membership: `,"audienceMembership": {"beta": {"member": true}}`,
			wantValue: true, wantStatus: "HEALTHY",
		},
		{
			name: "simulated non member", membership: `,"audienceMembership": {"beta": {"member": false}}`,
			wantValue: false, wantStatus: "HEALTHY",
		},
		{
			name: "list not simulated: unknown", membership: `,"audienceMembership": {"other": {"member": true}}`,
			wantValue: false, wantStatus: "STORE_ERROR",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"flagName":"f","context":{"key":"user-1"},"flag":` + audienceFlag + tt.membership + `}`
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/v1/feature/evaluate", strings.NewReader(body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			require.NoError(t, EvaluateHandler(e.NewContext(req, rec)))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var res model.VariationResult[any]
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
			assert.Equal(t, tt.wantValue, res.Value)
			assert.Equal(t, tt.wantStatus, res.Metadata[flag.AudienceStatusMetadataKey])
		})
	}
}

func TestSimulateAudienceMembership_missingAttribute(t *testing.T) {
	var f flag.InternalFlag
	require.NoError(t, json.Unmarshal([]byte(`{
  "variations": {"A": false, "B": true},
  "targeting": [{"name": "r", "query": "email in audience(\"vip\")", "variation": "B"}],
  "defaultRule": {"variation": "A"}
}`), &f))
	evalCtx := ffcontext.NewEvaluationContext("user-1")
	got := simulateAudienceMembership(&f, evalCtx, map[string]AudienceSimulation{"vip": {Member: true}})
	assert.Empty(t, got, "the attribute is not in the context: the term stays unknown")
}

func TestWithAudienceStatus(t *testing.T) {
	tests := []struct {
		name       string
		flagMeta   map[string]any
		resolution map[string]any
		want       map[string]any
	}{
		{name: "no status", flagMeta: map[string]any{"a": 1}, resolution: map[string]any{}, want: map[string]any{"a": 1}},
		{
			name:       "status added",
			flagMeta:   map[string]any{"a": 1},
			resolution: map[string]any{flag.AudienceStatusMetadataKey: "HEALTHY"},
			want:       map[string]any{"a": 1, flag.AudienceStatusMetadataKey: "HEALTHY"},
		},
		{
			name:       "status added to empty metadata",
			resolution: map[string]any{flag.AudienceStatusMetadataKey: "STORE_ERROR"},
			want:       map[string]any{flag.AudienceStatusMetadataKey: "STORE_ERROR"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, withAudienceStatus(tt.flagMeta, tt.resolution))
		})
	}
}
