package scheduler

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func TestConvertRecommendationsSourceAccount(t *testing.T) {
	t.Parallel()
	for _, cloud := range []string{"aws", "azure", "gcp"} {
		t.Run(cloud, func(t *testing.T) {
			s := &Scheduler{}
			input := []common.Recommendation{
				{Account: " SourceAbC/é\t", Service: common.ServiceEC2, Term: "1yr"},
				{Account: "sourceabc/e\u0301", Service: common.ServiceEC2, Term: "1yr"},
				{Service: common.ServiceEC2, Term: "1yr"},
			}
			records := s.convertRecommendations(input, cloud)
			require.Len(t, records, 3)
			input[0].Account = "changed"
			tagged := s.tagAccount(records, "11111111-1111-4111-8111-111111111111")
			require.Len(t, tagged, 3)
			for i, want := range []string{" SourceAbC/é\t", "sourceabc/e\u0301", ""} {
				raw, err := json.Marshal(tagged[i])
				require.NoError(t, err)
				var wire map[string]any
				require.NoError(t, json.Unmarshal(raw, &wire))
				require.Equal(t, "11111111-1111-4111-8111-111111111111", wire["cloud_account_id"])
				if want == "" {
					require.NotContains(t, wire, "source_account_id")
				} else {
					require.Equal(t, want, wire["source_account_id"])
				}
			}
		})
	}
}

func TestRecommendationSourceAccountLegacyJSON(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{}`, `{"source_account_id":null}`} {
		var record config.RecommendationRecord
		require.NoError(t, json.Unmarshal([]byte(raw), &record))
		encoded, err := json.Marshal(record)
		require.NoError(t, err)
		var wire map[string]any
		require.NoError(t, json.Unmarshal(encoded, &wire))
		require.NotContains(t, wire, "source_account_id")
	}
}

func TestRecommendationSourceIdentity(t *testing.T) {
	t.Parallel()
	for _, cloud := range []string{"aws", "azure", "gcp"} {
		base := config.RecommendationRecord{Provider: cloud, Service: "ec2", Region: "region", ResourceType: "sku", Term: 1}
		for _, suffix := range []string{"", "zone|é"} {
			base.AvailabilityZone = suffix
			base.Details = json.RawMessage(`{"platform":"Linux/UNIX","tenancy":"default"}`)
			legacy, err := recommendationID(base)
			require.NoError(t, err)
			empty := base
			empty.SourceAccountID = new("")
			emptyID, err := recommendationID(empty)
			require.NoError(t, err)
			require.Equal(t, legacy, emptyID)
			ids := map[string]bool{}
			for _, source := range []string{"SourceA", "sourcea", " source|a\t", "é", "e\u0301"} {
				rec := base
				rec.SourceAccountID = &source
				id, err := recommendationID(rec)
				require.NoError(t, err)
				require.NotEqual(t, legacy, id)
				require.False(t, ids[id])
				ids[id] = true
				require.True(t, strings.HasPrefix(id, "source-v1:"))
				decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "source-v1:"))
				require.NoError(t, err)
				require.Equal(t, strconv.Itoa(len(legacy))+":"+legacy+source, string(decoded))
				repeat, err := recommendationID(rec)
				require.NoError(t, err)
				require.Equal(t, id, repeat)
				oldKey, err := config.RecIdentityKey(&base)
				require.NoError(t, err)
				sourceKey, err := config.RecIdentityKey(&rec)
				require.NoError(t, err)
				require.Equal(t, oldKey, sourceKey)
			}
		}
	}
}

func TestRecommendationSourceIdentityFraming(t *testing.T) {
	t.Parallel()
	base := config.RecommendationRecord{Provider: "aws", Service: "rds", Term: 1}
	legacy, err := recommendationID(base)
	require.NoError(t, err)
	known := base
	known.SourceAccountID = new("source")
	knownID, err := recommendationID(known)
	require.NoError(t, err)
	legacyAZ := base
	legacyAZ.AvailabilityZone = base64.RawURLEncoding.EncodeToString([]byte("source"))
	legacyAZID, err := recommendationID(legacyAZ)
	require.NoError(t, err)
	require.Equal(t, legacy+"|"+legacyAZ.AvailabilityZone, legacyAZID)
	require.NotEqual(t, legacyAZID, knownID)
	first, second := base, base
	first.Details = json.RawMessage(`{"platform":"Linux/UNIX","tenancy":"a"}`)
	second.Details = json.RawMessage(`{"platform":"Linux/UNIX","tenancy":"ab"}`)
	firstLegacy, err := recommendationID(first)
	require.NoError(t, err)
	secondLegacy, err := recommendationID(second)
	require.NoError(t, err)
	require.Equal(t, firstLegacy+"bc", secondLegacy+"c")
	first.SourceAccountID, second.SourceAccountID = new("bc"), new("c")
	firstID, err := recommendationID(first)
	require.NoError(t, err)
	secondID, err := recommendationID(second)
	require.NoError(t, err)
	require.NotEqual(t, firstID, secondID)
}
