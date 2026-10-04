//go:build integration

package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type planEditReadBarrier struct {
	*config.PostgresStore
	afterRead func()
}

func (s *planEditReadBarrier) GetPurchasePlan(ctx context.Context, id string) (*config.PurchasePlan, error) {
	plan, err := s.PostgresStore.GetPurchasePlan(ctx, id)
	if err == nil && s.afterRead != nil {
		afterRead := s.afterRead
		s.afterRead = nil
		afterRead()
	}
	return plan, err
}

func TestPlanEditsPreserveConcurrentRampCompletion(t *testing.T) {
	for _, method := range []string{"PUT", "PATCH", "disable"} {
		for _, race := range []bool{false, true} {
			name := method + "/fresh"
			if race {
				name = method + "/stale"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				f := newCreateConcurrencyFixture(ctx, t)
				plan, err := f.store.GetPurchasePlan(ctx, f.planID)
				require.NoError(t, err)
				plan.Enabled = true
				plan.Services = map[string]config.ServiceConfig{"ec2": {Provider: "aws", Service: "ec2"}}
				plan.RampSchedule.Type = "custom"
				next := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)
				plan.NextExecutionDate = &next
				require.NoError(t, f.store.UpdatePurchasePlan(ctx, plan))
				require.NoError(t, f.store.SavePurchaseExecution(ctx, &config.PurchaseExecution{
					ExecutionID: uuid.NewString(), PlanID: f.planID, Status: "completed", StepNumber: 3, ScheduledDate: next,
				}))
				barrier := &planEditReadBarrier{PostgresStore: f.store}
				var completed *config.PurchasePlan
				if race {
					barrier.afterRead = func() {
						require.NoError(t, f.store.CompletePlanStep(ctx, f.planID, 3))
						completed, err = f.store.GetPurchasePlan(ctx, f.planID)
						require.NoError(t, err)
						require.Equal(t, 3, completed.RampSchedule.CurrentStep)
					}
				}
				f.handler.config = barrier
				req := &events.LambdaFunctionURLRequest{
					Headers: map[string]string{"Authorization": "Bearer admin-token", "Content-Type": "application/json"},
					Body:    `{"name":"renamed","enabled":true,"ramp_schedule":"custom","custom_step_percent":25,"custom_interval_days":7}`,
				}
				switch method {
				case "PUT":
					_, err = f.handler.updatePlan(ctx, req, f.planID)
				case "PATCH":
					req.Body = `{"name":"renamed"}`
					_, err = f.handler.patchPlan(ctx, req, f.planID)
				case "disable":
					err = f.handler.disablePlan(ctx, f.planID)
				}
				after, getErr := f.store.GetPurchasePlan(ctx, f.planID)
				require.NoError(t, getErr)
				if race {
					clientErr, ok := IsClientError(err)
					require.True(t, ok, "stale edit must return a client error, got %v", err)
					require.Equal(t, http.StatusConflict, clientErr.code)
					require.Equal(t, completed, after, "the winning completion must remain unchanged")
					return
				}
				require.NoError(t, err)
				if method == "disable" {
					require.False(t, after.Enabled)
				} else {
					require.Equal(t, "renamed", after.Name)
				}
				require.Equal(t, 2, after.RampSchedule.CurrentStep)
			})
		}
	}
}
