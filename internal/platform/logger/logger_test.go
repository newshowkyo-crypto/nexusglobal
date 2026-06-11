package logger

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew_Debug(t *testing.T) {
	l := New("debug")
	assert.NotNil(t, l)
}

func TestNew_Info(t *testing.T) {
	l := New("info")
	assert.NotNil(t, l)
}

func TestNew_Warn(t *testing.T) {
	l := New("warn")
	assert.NotNil(t, l)
}

func TestNew_Error(t *testing.T) {
	l := New("error")
	assert.NotNil(t, l)
}

func TestNew_Default(t *testing.T) {
	l := New("unknown")
	assert.NotNil(t, l)
}

func TestNew_Empty(t *testing.T) {
	l := New("")
	assert.NotNil(t, l)
}

func TestNew_ReturnsSlogLogger(t *testing.T) {
	l := New("info")
	var _ *slog.Logger = l
}
