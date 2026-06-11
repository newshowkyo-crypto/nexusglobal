package clock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRealClock_Now(t *testing.T) {
	c := RealClock{}
	before := time.Now().UTC()
	got := c.Now()
	after := time.Now().UTC()
	assert.False(t, got.Before(before))
	assert.False(t, got.After(after))
}

func TestMockClock_Now(t *testing.T) {
	fixed := time.Date(2026, 6, 12, 8, 0, 0, 0, time.UTC)
	c := NewMockClock(fixed)
	assert.Equal(t, fixed, c.Now())
}

func TestMockClock_Set(t *testing.T) {
	c := NewMockClock(time.Time{})
	newTime := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	c.Set(newTime)
	assert.Equal(t, newTime, c.Now())
}

func TestMockClock_ImplementsClock(t *testing.T) {
	var _ Clock = &MockClock{}
	var _ Clock = RealClock{}
}
