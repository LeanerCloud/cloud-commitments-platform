package api

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Field-drop guards (#19): a field added to an API request struct must reach
// the store struct, or be listed with a reason in the adapter's allowlist.

var timeType = reflect.TypeOf(time.Time{})

// zeroTimes clears every settable time.Time reachable from v, so two
// conversions made at different moments compare equal unless a field value
// differs. It compares by reflection, not JSON, so json:"-" store fields
// (credentials, tokens) are seen too.
func zeroTimes(v reflect.Value) {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			zeroTimes(v.Elem())
		}
	case reflect.Struct:
		if v.Type() == timeType {
			if v.CanSet() {
				v.Set(reflect.Zero(timeType))
			}
			return
		}
		for i := 0; i < v.NumField(); i++ {
			zeroTimes(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			zeroTimes(v.Index(i))
		}
	}
}

func normalized[Out any](out Out) Out {
	zeroTimes(reflect.ValueOf(&out).Elem())
	return out
}

// requestFieldName is the JSON key encoding/json binds the field to: its tag
// name, or its Go name when untagged. Fields json never binds return "".
func requestFieldName(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	switch name {
	case "-":
		return ""
	case "":
		return f.Name
	}
	return name
}

// setDistinct sets f to a non-zero value derived from tag, so it differs from
// a base request that leaves the field unset.
func setDistinct(t *testing.T, f reflect.Value, tag string) {
	t.Helper()
	switch f.Kind() {
	case reflect.String:
		f.SetString("v-" + tag)
	case reflect.Bool:
		f.SetBool(true)
	case reflect.Int:
		f.SetInt(11)
	case reflect.Float64:
		f.SetFloat(7.5)
	case reflect.Slice:
		if f.Type().Elem().Kind() != reflect.String {
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
			return
		}
		f.Set(reflect.ValueOf([]string{"v-" + tag}))
	case reflect.Ptr:
		p := reflect.New(f.Type().Elem())
		setDistinct(t, p.Elem(), tag)
		f.Set(p)
	default:
		t.Fatalf("field %s: unsupported kind %s, extend setDistinct", tag, f.Kind())
	}
}

// requireFieldsReach sets each JSON-bindable field of the request on top of
// base, one at a time, and requires convert's output to change unless the
// field is allowlisted. An allowlisted field must NOT change the output, so a
// fixed drop leaves a stale entry that fails.
func requireFieldsReach[Req, Out any](t *testing.T, base Req, convert func(Req) Out, notMapped map[string]string) {
	t.Helper()
	typ := reflect.TypeOf(base)
	tags := map[string]int{}
	for i := 0; i < typ.NumField(); i++ {
		if name := requestFieldName(typ.Field(i)); name != "" {
			tags[name] = i
		}
	}
	for tag := range notMapped {
		_, ok := tags[tag]
		require.Truef(t, ok, "allowlist entry %q is not a json field of %s", tag, typ.Name())
	}
	baseOut := normalized(convert(base))
	for tag, idx := range tags {
		t.Run(tag, func(t *testing.T) {
			req := base
			setDistinct(t, reflect.ValueOf(&req).Elem().Field(idx), tag)
			changed := !reflect.DeepEqual(normalized(convert(req)), baseOut)
			if reason, skip := notMapped[tag]; skip {
				assert.Falsef(t, changed, "%s now reaches the target; remove its allowlist entry (%s)", tag, reason)
				return
			}
			assert.Truef(t, changed, "%s is dropped by the converter: add the copy, or allowlist it with a reason", tag)
		})
	}
}

func TestCloudAccountFromRequest_NoFieldDropped(t *testing.T) {
	t.Parallel()
	requireFieldsReach(t, CloudAccountRequest{}, cloudAccountFromRequest, map[string]string{})
}

func TestRegistrationFromRequest_NoFieldDropped(t *testing.T) {
	t.Parallel()
	requireFieldsReach(t, RegistrationRequest{},
		func(r RegistrationRequest) *config.AccountRegistration { return registrationFromRequest(r, "tok") },
		map[string]string{
			"credential_type":    "encrypted and attached by submitRegistration, not the mapper",
			"credential_payload": "encrypted and attached by submitRegistration, not the mapper",
		})
}

func TestPlanRequestToPurchasePlan_NoFieldDropped(t *testing.T) {
	t.Parallel()
	// provider, service, term, payment and target_coverage fold into Services;
	// the custom ramp base makes custom_step_percent and custom_interval_days
	// visible in RampSchedule.
	base := PlanRequest{Provider: "aws", Service: "ec2", RampSchedule: "custom"}
	requireFieldsReach(t, base,
		func(r PlanRequest) *config.PurchasePlan { return r.toPurchasePlan() },
		map[string]string{
			"target_accounts": "handled by createPlan/setPlanAccounts, not the converter",
			"groups":          "the converter ignores groups until the handler builds plans from them (#1746)",
		})
}
