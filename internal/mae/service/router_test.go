package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexusglobal/mamr/internal/mae/repository/db"
)

type mockRepo struct {
	configs []db.MaeConfiguration
}

func (m *mockRepo) InsertConfig(_ context.Context, _ db.InsertMAEConfigParams) (*db.MaeConfiguration, error) {
	return nil, nil
}
func (m *mockRepo) GetConfig(_ context.Context, _ uuid.UUID) (*db.MaeConfiguration, error) {
	return nil, nil
}
func (m *mockRepo) GetConfigByMAEID(_ context.Context, _ string) (*db.MaeConfiguration, error) {
	return nil, nil
}
func (m *mockRepo) ListConfigs(_ context.Context) ([]db.MaeConfiguration, error) {
	return m.configs, nil
}
func (m *mockRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _, _ string, _ int32, _ *time.Time) error {
	return nil
}
func (m *mockRepo) RotateSecret(_ context.Context, _ uuid.UUID, _, _ []byte, _ int32) error {
	return nil
}

func (m *mockRepo) UpdateConfig(_ context.Context, _ db.UpdateMAEConfigParams) error {
	return nil
}

func toJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestRouter_Select_ActiveOnly(t *testing.T) {
	repo := &mockRepo{
		configs: []db.MaeConfiguration{
			{ID: uuid.New(), MaeID: "mae1", Status: "Active", RoutingWeight: 100, SupportedCurrencies: toJSON([]string{"USD"})},
			{ID: uuid.New(), MaeID: "mae2", Status: "Draft", RoutingWeight: 100, SupportedCurrencies: toJSON([]string{"USD"})},
			{ID: uuid.New(), MaeID: "mae3", Status: "Cooldown", RoutingWeight: 100, SupportedCurrencies: toJSON([]string{"USD"})},
		},
	}

	router := NewRouter(repo)
	result, err := router.Select(context.Background(), RouteRequest{Currency: "USD", TenantID: "t1"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "mae1", result.MAE.MaeID)
	assert.False(t, result.IsTesting)
}

func TestRouter_Select_CurrencyFilter(t *testing.T) {
	repo := &mockRepo{
		configs: []db.MaeConfiguration{
			{ID: uuid.New(), MaeID: "usd_only", Status: "Active", RoutingWeight: 100, SupportedCurrencies: toJSON([]string{"USD"})},
			{ID: uuid.New(), MaeID: "eur_only", Status: "Active", RoutingWeight: 100, SupportedCurrencies: toJSON([]string{"EUR"})},
		},
	}

	router := NewRouter(repo)
	result, err := router.Select(context.Background(), RouteRequest{Currency: "EUR", TenantID: "t1"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "eur_only", result.MAE.MaeID)
}

func TestRouter_Select_TenantScope(t *testing.T) {
	repo := &mockRepo{
		configs: []db.MaeConfiguration{
			{ID: uuid.New(), MaeID: "t1_only", Status: "Active", RoutingWeight: 100, TenantScope: toJSON([]string{"t1"})},
			{ID: uuid.New(), MaeID: "t2_only", Status: "Active", RoutingWeight: 100, TenantScope: toJSON([]string{"t2"})},
		},
	}

	router := NewRouter(repo)
	result, err := router.Select(context.Background(), RouteRequest{Currency: "USD", TenantID: "t2"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "t2_only", result.MAE.MaeID)
}

func TestRouter_Select_WeightDistribution(t *testing.T) {
	repo := &mockRepo{
		configs: []db.MaeConfiguration{
			{ID: uuid.New(), MaeID: "heavy", Status: "Active", RoutingWeight: 90},
			{ID: uuid.New(), MaeID: "light", Status: "Active", RoutingWeight: 10},
		},
	}

	router := NewRouter(repo)
	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		result, err := router.Select(context.Background(), RouteRequest{Currency: "USD", TenantID: "t1"})
		require.NoError(t, err)
		require.NotNil(t, result)
		counts[result.MAE.MaeID]++
	}

	assert.Greater(t, counts["heavy"], 700, "heavy should get ~90%% of traffic")
	assert.Less(t, counts["light"], 300, "light should get ~10%% of traffic")
}

func TestRouter_Select_TestingGrayscale(t *testing.T) {
	repo := &mockRepo{
		configs: []db.MaeConfiguration{
			{ID: uuid.New(), MaeID: "active1", Status: "Active", RoutingWeight: 100, TestingTrafficBps: 500},
			{ID: uuid.New(), MaeID: "testing1", Status: "Testing", RoutingWeight: 100, TestingTrafficBps: 500},
		},
	}

	router := NewRouter(repo)
	testingCount := 0
	for i := 0; i < 10000; i++ {
		result, err := router.Select(context.Background(), RouteRequest{Currency: "USD", TenantID: "t1"})
		require.NoError(t, err)
		require.NotNil(t, result)
		if result.IsTesting {
			testingCount++
		}
	}

	assert.InDelta(t, 500, testingCount, 200, "testing should get ~5%% of traffic (500 bps out of 10000)")
}

func TestRouter_Select_SuspendedExcluded(t *testing.T) {
	repo := &mockRepo{
		configs: []db.MaeConfiguration{
			{ID: uuid.New(), MaeID: "suspended", Status: "Suspended", RoutingWeight: 100},
			{ID: uuid.New(), MaeID: "disabled", Status: "Disabled", RoutingWeight: 100},
		},
	}

	router := NewRouter(repo)
	result, err := router.Select(context.Background(), RouteRequest{Currency: "USD", TenantID: "t1"})

	require.NoError(t, err)
	assert.Nil(t, result, "no candidates should be available")
}

func TestRouter_Select_NoMatch(t *testing.T) {
	repo := &mockRepo{
		configs: []db.MaeConfiguration{
			{ID: uuid.New(), MaeID: "eur_only", Status: "Active", RoutingWeight: 100, SupportedCurrencies: toJSON([]string{"EUR"})},
		},
	}

	router := NewRouter(repo)
	result, err := router.Select(context.Background(), RouteRequest{Currency: "USD", TenantID: "t1"})

	require.NoError(t, err)
	assert.Nil(t, result)
}
