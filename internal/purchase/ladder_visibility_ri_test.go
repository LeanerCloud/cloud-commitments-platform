package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	sptypes "github.com/aws/aws-sdk-go-v2/service/savingsplans/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// riFake answers DescribeReservedInstances per account, like AWS does: an ID
// owned by another account is simply not returned.
type riFake struct {
	byAcct       map[string][]ec2types.ReservedInstances
	err          error
	factoryErr   error
	gotFilters   []ec2types.Filter
	gotIDs       []string
	factoryAccts []string
	factoryRgns  []string
	calls        int
}

type riAcctDescriber struct {
	f    *riFake
	acct string
}

func (d riAcctDescriber) DescribeReservedInstances(_ context.Context, in *ec2.DescribeReservedInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeReservedInstancesOutput, error) {
	d.f.calls++
	d.f.gotFilters, d.f.gotIDs = in.Filters, in.ReservedInstancesIds
	if d.f.err != nil {
		return nil, d.f.err
	}
	return &ec2.DescribeReservedInstancesOutput{ReservedInstances: d.f.byAcct[d.acct]}, nil
}

func (f *riFake) factory(_ context.Context, acct, region string) (RIDescriber, error) {
	f.factoryAccts = append(f.factoryAccts, acct)
	f.factoryRgns = append(f.factoryRgns, region)
	if f.factoryErr != nil {
		return nil, f.factoryErr
	}
	return riAcctDescriber{f: f, acct: acct}, nil
}

func ri(id string, state ec2types.ReservedInstanceState, class ec2types.OfferingClassType, az string) ec2types.ReservedInstances {
	r := ec2types.ReservedInstances{
		ReservedInstancesId: aws.String(id), State: state, OfferingClass: class,
		FixedPrice: aws.Float32(0), UsagePrice: aws.Float32(0.1), Duration: aws.Int64(31536000), InstanceCount: aws.Int32(1),
	}
	if az != "" {
		r.AvailabilityZone = aws.String(az)
	}
	return r
}

func activeRI(id string) ec2types.ReservedInstances {
	return ri(id, ec2types.ReservedInstanceStateActive, ec2types.OfferingClassTypeConvertible, "")
}

func riRun(t *testing.T, v *visFake, r *riFake) LadderReconcileResult {
	t.Helper()
	res, err := RunLadderReconcile(context.Background(), LadderReconcileDeps{Store: v, SP: v.factory, RI: r.factory, Now: time.Now})
	require.NoError(t, err)
	return res
}

func TestRIVisibility_ActiveConvertibleCompletes(t *testing.T) {
	v, r := &visFake{}, &riFake{}
	v.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
	r.byAcct = map[string][]ec2types.ReservedInstances{"acct-a": {activeRI("ri-1")}}
	riRun(t, v, r)
	assert.Equal(t, []string{"t1"}, v.completed)
	assert.Equal(t, []string{"ri-1"}, r.gotIDs)
	assert.Equal(t, []string{"us-east-1"}, r.factoryRgns)
	assert.Zero(t, v.calls, "an RI tranche must not call the Savings Plans API")
	names := map[string][]string{}
	for _, f := range r.gotFilters {
		names[aws.ToString(f.Name)] = f.Values
	}
	assert.Equal(t, map[string][]string{"state": {"active"}, "offering-class": {"convertible"}}, names)
}

func TestRIVisibility_StaysFired(t *testing.T) {
	conv, std := ec2types.OfferingClassTypeConvertible, ec2types.OfferingClassTypeStandard
	cases := map[string][]ec2types.ReservedInstances{
		"missing":        nil,
		"other id":       {activeRI("ri-other")},
		"pending":        {ri("ri-1", ec2types.ReservedInstanceStatePaymentPending, conv, "")},
		"retired":        {ri("ri-1", ec2types.ReservedInstanceStateRetired, conv, "")},
		"payment failed": {ri("ri-1", ec2types.ReservedInstanceStatePaymentFailed, conv, "")},
		"queued":         {ri("ri-1", ec2types.ReservedInstanceStateQueued, conv, "")},
		"standard class": {ri("ri-1", ec2types.ReservedInstanceStateActive, std, "")},
		"other region":   {ri("ri-1", ec2types.ReservedInstanceStateActive, conv, "eu-west-1a")},
	}
	for name, ris := range cases {
		t.Run(name, func(t *testing.T) {
			v, r := &visFake{}, &riFake{byAcct: map[string][]ec2types.ReservedInstances{"acct-a": ris}}
			v.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
			riRun(t, v, r)
			assert.Empty(t, v.completed)
		})
	}
}

// Netting reads an unreported price field as 0, so such an RI must not end its
// tranche's netting.
func TestRIVisibility_MissingPriceFieldStaysFired(t *testing.T) {
	strip := map[string]func(*ec2types.ReservedInstances){
		"fixed price":    func(r *ec2types.ReservedInstances) { r.FixedPrice = nil },
		"usage price":    func(r *ec2types.ReservedInstances) { r.UsagePrice = nil },
		"duration":       func(r *ec2types.ReservedInstances) { r.Duration = nil },
		"instance count": func(r *ec2types.ReservedInstances) { r.InstanceCount = nil },
	}
	for name, f := range strip {
		t.Run(name, func(t *testing.T) {
			x := activeRI("ri-1")
			f(&x)
			v, r := &visFake{}, &riFake{byAcct: map[string][]ec2types.ReservedInstances{"acct-a": {x}}}
			v.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
			riRun(t, v, r)
			assert.Empty(t, v.completed)
		})
	}
	zero := activeRI("ri-1")
	zero.InstanceCount = aws.Int32(0)
	upfrontNoDuration := activeRI("ri-1")
	upfrontNoDuration.FixedPrice, upfrontNoDuration.Duration = aws.Float32(100), aws.Int64(0)
	for name, x := range map[string]ec2types.ReservedInstances{"zero count": zero, "upfront without duration": upfrontNoDuration} {
		v, r := &visFake{}, &riFake{byAcct: map[string][]ec2types.ReservedInstances{"acct-a": {x}}}
		v.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
		riRun(t, v, r)
		assert.Empty(t, v.completed, name)
	}
	// No upfront fee needs no duration.
	noUp := activeRI("ri-1")
	noUp.Duration = aws.Int64(0)
	vn, rn := &visFake{}, &riFake{byAcct: map[string][]ec2types.ReservedInstances{"acct-a": {noUp}}}
	vn.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
	riRun(t, vn, rn)
	assert.Equal(t, []string{"t1"}, vn.completed)
	// A recurring charge amount alone satisfies the recurring field.
	x := activeRI("ri-1")
	x.UsagePrice = nil
	x.RecurringCharges = []ec2types.RecurringCharge{{Amount: aws.Float64(0.05)}}
	v, r := &visFake{}, &riFake{byAcct: map[string][]ec2types.ReservedInstances{"acct-a": {x}}}
	v.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
	riRun(t, v, r)
	assert.Equal(t, []string{"t1"}, v.completed)
}

func TestRIVisibility_ZonalInRegionCompletes(t *testing.T) {
	v := &visFake{}
	v.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
	r := &riFake{byAcct: map[string][]ec2types.ReservedInstances{"acct-a": {ri("ri-1", ec2types.ReservedInstanceStateActive, ec2types.OfferingClassTypeConvertible, "us-east-1b")}}}
	riRun(t, v, r)
	assert.Equal(t, []string{"t1"}, v.completed)
}

func TestRIVisibility_ErrorsFailClosed(t *testing.T) {
	for name, r := range map[string]*riFake{
		"api error":     {err: errors.New("throttled")},
		"factory error": {factoryErr: errors.New("no creds")},
	} {
		t.Run(name, func(t *testing.T) {
			v := &visFake{}
			v.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
			r.byAcct = map[string][]ec2types.ReservedInstances{"acct-a": {activeRI("ri-1")}}
			res := riRun(t, v, r)
			assert.Empty(t, v.completed)
			assert.Equal(t, 1, res.Unresolved)
		})
	}
}

func TestRIVisibility_NilFactoryFailsClosed(t *testing.T) {
	v := &visFake{}
	v.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
	_, err := RunLadderReconcile(context.Background(), LadderReconcileDeps{Store: v, SP: v.factory, Now: time.Now})
	require.NoError(t, err)
	assert.Empty(t, v.completed)
}

// The same reservation ID in another account must not complete this account's
// tranche: the lookup is made with the tranche's own account.
func TestRIVisibility_CrossAccountSameIDDoesNotComplete(t *testing.T) {
	v := &visFake{}
	v.addTranche("t-b", "acct-b", "ri-1", ladder.LayerConvertibleRI)
	r := &riFake{byAcct: map[string][]ec2types.ReservedInstances{"acct-a": {activeRI("ri-1")}}}
	riRun(t, v, r)
	assert.Empty(t, v.completed)
	assert.Equal(t, []string{"acct-b"}, r.factoryAccts)
}

func TestRIVisibility_SPAndRITranchesDispatchByLayer(t *testing.T) {
	v := &visFake{}
	v.addTranche("t-sp", "acct-a", "sp-1", ladder.LayerComputeSP)
	v.addTranche("t-ri", "acct-a", "ri-1", ladder.LayerConvertibleRI)
	v.plans = nil // SP "sp-1" is not visible; the RI is
	r := &riFake{byAcct: map[string][]ec2types.ReservedInstances{"acct-a": {activeRI("ri-1")}}}
	riRun(t, v, r)
	assert.Equal(t, []string{"t-ri"}, v.completed)
	v2 := &visFake{}
	v2.addTranche("t-sp", "acct-a", "sp-1", ladder.LayerComputeSP)
	v2.plans = []sptypes.SavingsPlan{sp("sp-1", sptypes.SavingsPlanTypeCompute, sptypes.SavingsPlanStateActive, "")}
	r2 := &riFake{}
	riRun(t, v2, r2)
	assert.Equal(t, []string{"t-sp"}, v2.completed)
	assert.Zero(t, r2.calls, "an SP tranche must not call the EC2 API")
}
