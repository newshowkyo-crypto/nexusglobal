package mae

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockMAE_HealthCheck_Success(t *testing.T) {
	m := NewMockMAE()
	res, err := m.HealthCheck(context.Background(), "mae_01")
	require.NoError(t, err)
	assert.Equal(t, "healthy", res.Status)
	assert.Greater(t, res.Latency, 0)
}

func TestMockMAE_HealthCheck_Timeout(t *testing.T) {
	m := NewMockMAE()
	m.SetDefaultScenario(ScenarioTimeout)
	_, err := m.HealthCheck(context.Background(), "mae_01")
	assert.ErrorIs(t, err, ErrTimeout)
}

func TestMockMAE_HealthCheck_Failure(t *testing.T) {
	m := NewMockMAE()
	m.SetDefaultScenario(ScenarioFailure)
	_, err := m.HealthCheck(context.Background(), "mae_01")
	assert.ErrorIs(t, err, ErrExplicitFailure)
}

func TestMockMAE_HealthCheck_PerKeyScenario(t *testing.T) {
	m := NewMockMAE()
	m.SetScenario("mae_bad", ScenarioFailure)

	_, err := m.HealthCheck(context.Background(), "mae_bad")
	assert.ErrorIs(t, err, ErrExplicitFailure)

	_, err = m.HealthCheck(context.Background(), "mae_good")
	assert.NoError(t, err)
}
