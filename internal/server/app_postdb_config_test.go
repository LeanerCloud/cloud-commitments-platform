package server

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

func TestPostDBPurchaseManagerConfig_SetsAttemptJournal(t *testing.T) {
	pgStore := &config.PostgresStore{}
	app := &Application{Config: pgStore}

	cfg := postDBPurchaseManagerConfig(app, pgStore, nil, aws.Config{})

	assert.Same(t, pgStore, cfg.AttemptJournal, "post-DB manager journals attempts in the config store")
	assert.Same(t, pgStore, cfg.ConfigStore)
}
