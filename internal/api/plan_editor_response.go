package api

import (
	"slices"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

type PlanEditorResponse struct {
	Plan                   PlanEditorStoredPlan    `json:"plan"`
	AccountIDs             []string                `json:"account_ids"`
	Accounts               []PlanEditorAccount     `json:"accounts"`
	Revision               string                  `json:"revision"`
	History                []PlanEditorExecution   `json:"history"`
	Graph                  *PlanEditorGraph        `json:"graph"`
	GraphUnavailableReason *PlanEditorReason       `json:"graph_unavailable_reason"`
	LedgerWitness          PlanEditorLedgerWitness `json:"ledger_witness"`
	FieldPolicy            PlanEditorFieldPolicy   `json:"field_policy"`
}

type PlanEditorStoredPlan struct {
	ID                     string                       `json:"id"`
	Name                   string                       `json:"name"`
	Enabled                bool                         `json:"enabled"`
	AutoPurchase           bool                         `json:"auto_purchase"`
	NotificationDaysBefore int                          `json:"notification_days_before"`
	Services               map[string]PlanEditorService `json:"services"`
	RampSchedule           PlanEditorRamp               `json:"ramp_schedule"`
	CreatedAt              time.Time                    `json:"created_at"`
	UpdatedAt              time.Time                    `json:"updated_at"`
	NextExecutionDate      *time.Time                   `json:"next_execution_date"`
	LastExecutionDate      *time.Time                   `json:"last_execution_date"`
	LastNotificationSent   *time.Time                   `json:"last_notification_sent"`
	MaxStepCommitment      *float64                     `json:"max_step_commitment"`
}

type PlanEditorService struct {
	Provider       string   `json:"provider"`
	Service        string   `json:"service"`
	Enabled        bool     `json:"enabled"`
	Term           int      `json:"term"`
	Payment        string   `json:"payment"`
	Coverage       float64  `json:"coverage"`
	RampSchedule   string   `json:"ramp_schedule"`
	IncludeEngines []string `json:"include_engines"`
	ExcludeEngines []string `json:"exclude_engines"`
	IncludeRegions []string `json:"include_regions"`
	ExcludeRegions []string `json:"exclude_regions"`
	IncludeTypes   []string `json:"include_types"`
	ExcludeTypes   []string `json:"exclude_types"`
	MinCount       int      `json:"min_count"`
}

type PlanEditorRamp struct {
	Type             string    `json:"type"`
	PercentPerStep   float64   `json:"percent_per_step"`
	StepIntervalDays int       `json:"step_interval_days"`
	CurrentStep      int       `json:"current_step"`
	TotalSteps       int       `json:"total_steps"`
	StartDate        time.Time `json:"start_date"`
}

type PlanEditorAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type PlanEditorGraph struct {
	CurrentStep                  int               `json:"current_step"`
	TotalSteps                   int               `json:"total_steps"`
	StructuralShapeChangeAllowed bool              `json:"structural_shape_change_allowed"`
	ShapeLockReason              *PlanEditorReason `json:"shape_lock_reason"`
	Steps                        []PlanEditorSlot  `json:"steps"`
}

type PlanEditorLedgerWitness struct {
	LedgerState          string `json:"ledger_state"`
	HasTrustedRampLedger bool   `json:"has_trusted_ramp_ledger"`
	BaselinePresent      bool   `json:"baseline_present"`
}

func buildPlanEditorResponse(snapshot *config.PlanEditorSnapshot, policy *planEditorFieldPolicyResult, history []PlanEditorExecution, revision string) *PlanEditorResponse {
	stored := snapshot.Plan
	ramp := stored.RampSchedule
	response := &PlanEditorResponse{
		Plan: PlanEditorStoredPlan{
			ID: stored.ID, Name: stored.Name, Enabled: stored.Enabled, AutoPurchase: stored.AutoPurchase,
			NotificationDaysBefore: stored.NotificationDaysBefore,
			RampSchedule: PlanEditorRamp{
				Type: ramp.Type, PercentPerStep: ramp.PercentPerStep, StepIntervalDays: ramp.StepIntervalDays,
				CurrentStep: ramp.CurrentStep, TotalSteps: ramp.TotalSteps, StartDate: ramp.StartDate,
			},
			CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
			NextExecutionDate: stored.NextExecutionDate, LastExecutionDate: stored.LastExecutionDate,
			LastNotificationSent: stored.LastNotificationSent, MaxStepCommitment: stored.MaxStepCommitment,
		},
		AccountIDs: append([]string{}, snapshot.AccountIDs...),
		Accounts:   make([]PlanEditorAccount, 0, len(snapshot.Accounts)),
		Revision:   revision, History: history,
		GraphUnavailableReason: policy.GraphUnavailableReason,
		LedgerWitness: PlanEditorLedgerWitness{
			LedgerState: string(snapshot.LedgerState), HasTrustedRampLedger: snapshot.HasTrustedRampLedger(),
			BaselinePresent: snapshot.Baseline != nil,
		},
		FieldPolicy: policy.Fields,
	}
	if stored.Services != nil {
		response.Plan.Services = make(map[string]PlanEditorService, len(stored.Services))
		for key := range stored.Services {
			service := stored.Services[key]
			response.Plan.Services[key] = PlanEditorService{
				Provider: service.Provider, Service: service.Service, Enabled: service.Enabled,
				Term: service.Term, Payment: service.Payment, Coverage: service.Coverage, RampSchedule: service.RampSchedule,
				IncludeEngines: slices.Clone(service.IncludeEngines), ExcludeEngines: slices.Clone(service.ExcludeEngines),
				IncludeRegions: slices.Clone(service.IncludeRegions), ExcludeRegions: slices.Clone(service.ExcludeRegions),
				IncludeTypes: slices.Clone(service.IncludeTypes), ExcludeTypes: slices.Clone(service.ExcludeTypes),
				MinCount: service.MinCount,
			}
		}
	}
	for _, account := range snapshot.Accounts {
		response.Accounts = append(response.Accounts, PlanEditorAccount{ID: account.ID, Name: account.Name, Provider: account.Provider})
	}
	if graph := policy.Graph; graph != nil {
		response.Graph = &PlanEditorGraph{
			CurrentStep: graph.CurrentStep, TotalSteps: graph.TotalSteps,
			StructuralShapeChangeAllowed: graph.StructuralShapeChangeAllowed, ShapeLockReason: graph.ShapeLockReason,
			Steps: graph.Steps,
		}
	}
	return response
}
