package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
)

type insuranceDiagnosticBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *insuranceDiagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *insuranceDiagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestInsuranceComparison_CredentialSafety(t *testing.T) {
	const adminKey = "synthetic-diagnostic-admin"
	const candidateID = "33333333-3333-4333-8333-333333333333"
	const premium = "103.1234567890123456789"
	capture := func(t *testing.T) []*insuranceDiagnosticBuffer {
		t.Helper()
		standard, structured, shared := &insuranceDiagnosticBuffer{}, &insuranceDiagnosticBuffer{}, &insuranceDiagnosticBuffer{}
		previousStandard, previousSlog, previousFlags := log.Writer(), slog.Default(), log.Flags()
		previousShared, previousLevel := logging.SetOutput(shared), logging.GetLevel()
		t.Cleanup(func() {
			slog.SetDefault(previousSlog)
			log.SetOutput(previousStandard)
			log.SetFlags(previousFlags)
			logging.SetOutput(previousShared)
			logging.SetLevelValue(previousLevel)
		})
		slog.SetDefault(slog.New(slog.NewJSONHandler(structured, nil)))
		log.SetOutput(standard)
		logging.SetLevelValue(logging.LevelDebug)
		return []*insuranceDiagnosticBuffer{standard, structured, shared}
	}
	request := func(key string) *events.LambdaFunctionURLRequest {
		return &events.LambdaFunctionURLRequest{
			Headers: map[string]string{"x-api-key": key},
			RequestContext: events.LambdaFunctionURLRequestContext{
				HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: "GET", Path: "/api/insurance/comparison"},
			},
		}
	}
	checkSinks := func(t *testing.T, response *events.LambdaFunctionURLResponse, logs []*insuranceDiagnosticBuffer) {
		t.Helper()
		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(response.Body), &parsed))
		require.NotNil(t, parsed)
		parsedJSON, err := json.Marshal(parsed)
		require.NoError(t, err)
		envelope, err := json.Marshal(response)
		require.NoError(t, err)
		for _, sink := range []struct{ name, value string }{
			{"response body", response.Body}, {"parsed body", string(parsedJSON)}, {"Lambda envelope", string(envelope)},
			{"standard log", logs[0].String()}, {"slog", logs[1].String()}, {"shared log", logs[2].String()},
		} {
			assert.NotContains(t, sink.value, insTestKey, "sink=%s", sink.name)
		}
	}
	for _, role := range []string{"current", "candidate"} {
		for _, field := range []string{"control", "guaranteed_display_name", "region", "commitment_type"} {
			t.Run(role+"/"+field, func(t *testing.T) {
				settings := completeEnv(t)
				logs := capture(t)
				var wire map[string]any
				decoder := json.NewDecoder(strings.NewReader(cmpBody))
				decoder.UseNumber()
				require.NoError(t, decoder.Decode(&wire))
				currentTotals := wire["current_totals"].(map[string]any)
				monthly := currentTotals["commitment_financials_monthly_rate"].(map[string]any)
				monthly["commitment_cost"].(map[string]any)["breakdown"].(map[string]any)["archera_premium"] = json.Number(premium)
				row := wire["data"].([]any)[0].(map[string]any)
				entry := row["current"].(map[string]any)
				offer := entry["offer"].(map[string]any)
				offer["guaranteed_display_name"], offer["region"], entry["commitment_type"] = "Ordinary offer", "eu-west-1", "ordinary-type"
				if role == "candidate" {
					cloned, err := json.Marshal(entry)
					require.NoError(t, err)
					cloneDecoder := json.NewDecoder(bytes.NewReader(cloned))
					cloneDecoder.UseNumber()
					var candidate map[string]any
					require.NoError(t, cloneDecoder.Decode(&candidate))
					entry = candidate
					entry["is_current"], entry["offer_id"] = false, candidateID
					row["candidates"] = []any{entry}
					offer = entry["offer"].(map[string]any)
				}
				want := "synthetic-prefix-" + insTestKey + "-suffix"
				switch field {
				case "control":
				case "commitment_type":
					entry[field] = want
				default:
					offer[field] = want
				}
				body, err := json.Marshal(wire)
				require.NoError(t, err)
				decoded, err := insurance.DecodeComparison(bytes.NewReader(body), insTestOrg, insTestPlan, time.Now())
				require.NoError(t, err, "setup: pinned decoder must accept exact bytes")
				require.Len(t, decoded.Rows, 1)
				decodedOffer := decoded.Rows[0].Current
				if role == "candidate" {
					require.Len(t, decoded.Rows[0].Candidates, 1)
					decodedOffer = decoded.Rows[0].Candidates[0]
				}
				require.NotNil(t, decodedOffer.SelectedAmount)
				require.Equal(t, "3", decodedOffer.SelectedAmount.RatString())
				wantPremium, ok := new(big.Rat).SetString(premium)
				require.True(t, ok)
				require.NotNil(t, decoded.Current.Monthly.Premium)
				require.Zero(t, decoded.Current.Monthly.Premium.Cmp(wantPremium))
				wantName, wantRegion, wantType := "Ordinary offer", "eu-west-1", "ordinary-type"
				switch field {
				case "guaranteed_display_name":
					wantName = want
				case "region":
					wantRegion = want
				case "commitment_type":
					wantType = want
				}
				require.NotNil(t, decodedOffer.GuaranteedDisplayName)
				require.Equal(t, wantName, *decodedOffer.GuaranteedDisplayName)
				require.NotNil(t, decodedOffer.Region)
				require.Equal(t, wantRegion, *decodedOffer.Region)
				require.Equal(t, wantType, decodedOffer.CommitmentType)
				vt := &vendorTransport{t: t, body: string(body)}
				h := cmpHandler(t, settings, vt)
				h.apiKey = adminKey
				response, err := h.HandleRequest(context.Background(), request(adminKey))
				require.NoError(t, err)
				require.NotNil(t, response)
				require.Equal(t, 1, vt.calls)
				require.Equal(t, http.MethodGet, vt.lastReq.Method)
				require.Equal(t, "https", vt.lastReq.URL.Scheme)
				require.Equal(t, "api.archera.ai", vt.lastReq.URL.Host)
				require.Equal(t, "/v1/org/"+insTestOrg+"/commitment-plans/"+insTestPlan+"/comparison", vt.lastReq.URL.Path)
				require.Empty(t, vt.lastReq.URL.RawQuery)
				require.True(t, vt.lastReq.Header.Get("x-api-key") == insTestKey, "key confined to intercepted header")
				if field != "control" {
					assert.Equal(t, http.StatusBadGateway, response.StatusCode, "credential-bearing success must be rejected")
					assert.JSONEq(t, `{"error":"Archera comparison withheld: contained credential material"}`, response.Body)
					var rejected map[string]any
					require.NoError(t, json.Unmarshal([]byte(response.Body), &rejected))
					assert.NotContains(t, rejected, "rows", "whole comparison must be withheld")
					checkSinks(t, response, logs)
					return
				}
				require.Equal(t, http.StatusOK, response.StatusCode)
				var dto archera.ComparisonDTO
				require.NoError(t, json.Unmarshal([]byte(response.Body), &dto))
				require.Len(t, dto.Rows, 1)
				require.Equal(t, cmpLineID, dto.Rows[0].LineItemID)
				require.Equal(t, insTestPlan, dto.PlanID)
				require.Equal(t, "11111111-1111-4111-8111-111111111111", dto.Rows[0].Current.OfferID)
				require.True(t, dto.Rows[0].Current.IsCurrent)
				returned := dto.Rows[0].Current
				if role == "candidate" {
					require.Len(t, dto.Rows[0].Candidates, 1)
					returned = dto.Rows[0].Candidates[0]
					require.Equal(t, candidateID, returned.OfferID)
					require.False(t, returned.IsCurrent)
					require.NotNil(t, dto.Rows[0].Current.ArcheraOfferName)
					require.Equal(t, "Ordinary offer", *dto.Rows[0].Current.ArcheraOfferName)
					require.NotNil(t, dto.Rows[0].Current.Region)
					require.Equal(t, "eu-west-1", *dto.Rows[0].Current.Region)
					require.Equal(t, "ordinary-type", dto.Rows[0].Current.CommitmentType)
				}
				require.NotNil(t, returned.ArcheraOfferName)
				require.Equal(t, wantName, *returned.ArcheraOfferName)
				require.NotNil(t, returned.Region)
				require.Equal(t, wantRegion, *returned.Region)
				require.Equal(t, wantType, returned.CommitmentType, "selected field reachability")
				require.NotNil(t, returned.UpfrontCost)
				require.Equal(t, "1200", *returned.UpfrontCost)
				require.NotNil(t, returned.Monthly.CommitmentCostTotal)
				require.Equal(t, "110", *returned.Monthly.CommitmentCostTotal)
				require.NotNil(t, dto.Current.Premium)
				require.Equal(t, premium, *dto.Current.Premium)
				require.Nil(t, returned.ContractTerm)
				require.Nil(t, returned.PaymentOption)
				require.Nil(t, returned.BreakevenDays)
				require.Nil(t, returned.LeaseMenuItemID)
				require.Nil(t, returned.DeltaVsCurrent.BreakevenDays)
				require.Nil(t, dto.Currency)
				require.True(t, dto.PremiumIncluded)
				require.Equal(t, common.ArcheraNonGatingDisclosure, dto.NonGatingDisclosure)
				require.Equal(t, common.ArcheraSponsorshipDisclosure, dto.SponsorshipDisclosure)
				t.Log("benign actual-request fields and exact financial values preserved")
				checkSinks(t, response, logs)
			})
		}
	}
	t.Run("negative controls", func(t *testing.T) {
		for _, name := range []string{"missing admin", "wrong admin", "unconfigured", "malformed term", "vendor 401"} {
			t.Run(name, func(t *testing.T) {
				settings := completeEnv(t)
				logs := capture(t)
				vt := &vendorTransport{t: t, body: cmpBody}
				key, wantStatus, calls := adminKey, http.StatusBadGateway, 1
				switch name {
				case "missing admin":
					key, wantStatus, calls = "", http.StatusUnauthorized, 0
				case "wrong admin":
					key, wantStatus, calls = "synthetic-wrong-admin", http.StatusUnauthorized, 0
				case "unconfigured":
					settings, wantStatus, calls = archera.Settings{}, http.StatusServiceUnavailable, 0
				case "malformed term":
					vt.body = strings.Replace(cmpBody, `"contract_term":null`, `"contract_term":"invalid-`+insTestKey+`"`, 1)
					_, err := insurance.DecodeComparison(strings.NewReader(vt.body), insTestOrg, insTestPlan, time.Now())
					require.Error(t, err, "malformed contract must fail real decoder")
				case "vendor 401":
					vt.status, vt.body = http.StatusUnauthorized, `{"message":"bad key `+insTestKey+`"}`
				}
				h := cmpHandler(t, settings, vt)
				h.apiKey = adminKey
				req := request(key)
				if name == "missing admin" {
					delete(req.Headers, "x-api-key")
				}
				response, err := h.HandleRequest(context.Background(), req)
				require.NoError(t, err)
				require.NotNil(t, response)
				require.Equal(t, wantStatus, response.StatusCode)
				require.Equal(t, calls, vt.calls)
				checkSinks(t, response, logs)
			})
		}
	})
	t.Run("missing bound capability", func(t *testing.T) {
		logs := capture(t)
		vt := &vendorTransport{t: t, body: cmpBody}
		raw, err := insurance.NewClient(insurance.Config{APIKey: insurance.NewSecret(insTestKey), OrgID: insTestOrg}, &http.Client{Transport: vt})
		require.NoError(t, err)
		h := NewHandler(HandlerConfig{Insurance: &recProvider{client: raw}})
		h.apiKey = adminKey
		response, err := h.HandleRequest(context.Background(), request(adminKey))
		require.NoError(t, err)
		require.NotNil(t, response)
		require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
		assert.JSONEq(t, `{"error":"Archera comparison is not supported"}`, response.Body)
		assert.Zero(t, vt.calls, "missing bound capability must fail before HTTP")
		checkSinks(t, response, logs)
	})
}
