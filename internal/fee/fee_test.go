package fee

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompute(t *testing.T) {
	// 250 bps = 2.5%；10000 * 2.5% = 250
	assert.Equal(t, int64(250), Compute(10000, 250, 0, 0, 0))

	// + 固定费 30
	assert.Equal(t, int64(280), Compute(10000, 250, 30, 0, 0))

	// 四舍五入：9999 * 2.5% = 249.975 → 250
	assert.Equal(t, int64(250), Compute(9999, 250, 0, 0, 0))

	// 最低收费
	assert.Equal(t, int64(100), Compute(1000, 100, 0, 100, 0))

	// 封顶
	assert.Equal(t, int64(500), Compute(100000, 250, 0, 0, 500))

	// 不超过本金
	assert.Equal(t, int64(100), Compute(100, 10000, 0, 0, 0))

	// 非正本金
	assert.Equal(t, int64(0), Compute(0, 250, 10, 0, 0))
	assert.Equal(t, int64(0), Compute(-5, 250, 10, 0, 0))
}
