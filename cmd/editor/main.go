package main

import (
	"context"
	"net/http"
	"os"

	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	custommiddleware "github.com/thomaspoignant/go-feature-flag/cmdhelpers/api/middleware"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/log"
	"github.com/thomaspoignant/go-feature-flag/modules/core/dto"
	"github.com/thomaspoignant/go-feature-flag/modules/core/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/modules/core/model"
	"github.com/thomaspoignant/go-feature-flag/modules/core/utils"
	"go.uber.org/zap"
)

// This service is an API used to evaluate a flag with an evaluation context
// This API is made for the Flag Editor to be able to evaluate a flag remotely and see if the configuration
// of the flag is working as expected.

func main() {
	logger := log.InitLogger()
	defer func() { _ = logger.ZapLogger.Sync() }()

	e := echo.New()
	e.Use(custommiddleware.ZapLogger(logger.ZapLogger, false))
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{
			"https://gofeatureflag.org",
			"https://www.gofeatureflag.org",
		},
		AllowHeaders: []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept},
	}))

	g := e.Group("/v1")
	g.POST("/feature/evaluate", EvaluateHandler)
	e.GET("/health", HealthHandler)

	if _, exists := os.LookupEnv("RUN_AS_LAMBDA"); exists {
		adapter := awsLambdaHandler{adapter: httpadapter.NewV2(e)}
		adapter.Start()
	} else {
		if err := (echo.StartConfig{Address: ":1323"}).Start(context.Background(), e); err != nil {
			logger.ZapLogger.Fatal("error starting the editor API", zap.Error(err))
		}
	}
}

// EvaluateHandler is the function called when calling the endpoint /v1/feature/evaluate.
// It will perform a flag evaluation and return the resolutionDetails and the value.
func EvaluateHandler(c *echo.Context) error {
	u := new(editorEvaluateRequest)
	if err := c.Bind(u); err != nil {
		return err
	}
	f := u.Flag.Convert()
	evalCtx := utils.ConvertEvaluationCtxFromRequest(u.Context.Key, u.Context.Custom)
	value, resolutionDetails := f.Value(
		u.FlagName,
		evalCtx,
		flag.Context{
			DefaultSdkValue:    nil,
			AudienceMembership: simulateAudienceMembership(&f, evalCtx, u.AudienceMembership),
		},
	)
	resp := model.VariationResult[any]{
		Value:         value,
		VariationType: resolutionDetails.Variant,
		Reason:        resolutionDetails.Reason,
		ErrorCode:     resolutionDetails.ErrorCode,
		Failed:        resolutionDetails.ErrorCode != "",
		Cacheable:     resolutionDetails.Cacheable,
		Metadata:      withAudienceStatus(f.GetMetadata(), resolutionDetails.Metadata),
	}
	return c.JSON(http.StatusOK, resp)
}

// HealthHandler endpoint to validate that the service is up and running.
func HealthHandler(c *echo.Context) error {
	return c.String(http.StatusOK, "OK")
}

// editorEvaluateRequest is the format expected to receive from the editor to test the flag.
type editorEvaluateRequest struct {
	Context  ContextWrapper `json:"context,omitempty"`
	Flag     dto.DTO        `json:"flag,omitempty"`
	FlagName string         `json:"flagName,omitempty"`
	// AudienceMembership (optional) simulates the membership of the evaluation context in the
	// audience lists used by the flag, keyed by list name. The editor has no audience store:
	// without it, the audience terms are NOT_CONFIGURED, and a list that is not in the map is
	// unknown (STORE_ERROR).
	AudienceMembership map[string]AudienceSimulation `json:"audienceMembership,omitempty"`
}

// AudienceSimulation is the simulated membership of the evaluation context in one list.
type AudienceSimulation struct {
	Member bool `json:"member"`
}

// simulateAudienceMembership builds the membership of the audience terms of the flag from the
// simulation of the request. It returns nil (audiences not configured) when there is none.
func simulateAudienceMembership(
	f *flag.InternalFlag, evalCtx ffcontext.Context, sim map[string]AudienceSimulation,
) flag.AudienceMembership {
	if sim == nil {
		return nil
	}
	out := flag.AudienceMembership{}
	ctxMap := flag.AudienceContextMap(evalCtx)
	for _, t := range f.AudienceTerms() {
		s, ok := sim[t.List]
		if !ok {
			continue
		}
		value, ok := flag.AudienceAttributeValue(ctxMap, t.Attribute)
		if !ok {
			continue
		}
		out[flag.AudienceLookup{List: t.List, Value: value}] = flag.AudienceResult{Member: s.Member}
	}
	return out
}

// ContextWrapper is a struct to migrate the API request to an actual evaluation context.
type ContextWrapper struct {
	Key    string `json:"key,omitempty"`
	Custom map[string]any
}

// withAudienceStatus adds the audience status of the evaluation (if any) to the flag metadata.
func withAudienceStatus(flagMetadata, resolutionMetadata map[string]any) map[string]any {
	status, ok := resolutionMetadata[flag.AudienceStatusMetadataKey]
	if !ok {
		return flagMetadata
	}
	out := make(map[string]any, len(flagMetadata)+1)
	for k, v := range flagMetadata {
		out[k] = v
	}
	out[flag.AudienceStatusMetadataKey] = status
	return out
}
