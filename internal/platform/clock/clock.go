package clock

import "time"

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now().UTC()
}

type MockClock struct {
	fixedTime time.Time
}

func NewMockClock(t time.Time) *MockClock {
	return &MockClock{fixedTime: t}
}

func (m *MockClock) Now() time.Time {
	return m.fixedTime
}

func (m *MockClock) Set(t time.Time) {
	m.fixedTime = t
}
