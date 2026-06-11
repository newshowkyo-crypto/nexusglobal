package metric

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatBusinessDate(t *testing.T) {
	d := time.Date(2026, 6, 12, 8, 0, 0, 0, time.UTC)
	assert.Equal(t, "2026-06-12", FormatBusinessDate(d))
}

func TestFormatBusinessDate_Timezone(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	d := time.Date(2026, 6, 12, 4, 0, 0, 0, loc)
	assert.Equal(t, "2026-06-12", FormatBusinessDate(d))
}

func TestExposureKey_Format(t *testing.T) {
	key := exposureKey("mae_01", "2026-06-12")
	assert.Equal(t, "MAE_METRIC:mae_01:2026-06-12:gross_exposure_amount", key)
}

func TestCapturedKey_Format(t *testing.T) {
	key := capturedKey("mae_01", "2026-06-12")
	assert.Equal(t, "MAE_METRIC:mae_01:2026-06-12:captured_amount", key)
}

func TestRefundKey_Format(t *testing.T) {
	key := refundKey("mae_01", "2026-06-12")
	assert.Equal(t, "MAE_METRIC:mae_01:2026-06-12:refund_amount", key)
}

func TestDisputeKey_Format(t *testing.T) {
	key := disputeKey("mae_01", "2026-06-12")
	assert.Equal(t, "MAE_METRIC:mae_01:2026-06-12:dispute_amount", key)
}
