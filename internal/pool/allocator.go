package pool

import (
	"math"
	"strings"

	"github.com/nexusglobal/mamr/internal/pool/domain"
)

// Weights 打分权重。
type Weights struct {
	Success  float64
	Cost     float64
	Capacity float64
	Speed    float64
}

// DefaultWeights 默认权重：成功率优先，其次成本与容量。
func DefaultWeights() Weights {
	return Weights{Success: 0.50, Cost: 0.20, Capacity: 0.20, Speed: 0.10}
}

// WeightsFromMap 从策略 JSON 读取权重；缺失项用默认值。
func WeightsFromMap(m map[string]float64) Weights {
	w := DefaultWeights()
	if v, ok := m["success"]; ok {
		w.Success = v
	}
	if v, ok := m["cost"]; ok {
		w.Cost = v
	}
	if v, ok := m["capacity"]; ok {
		w.Capacity = v
	}
	if v, ok := m["speed"]; ok {
		w.Speed = v
	}
	return w
}

// applyStrategy 按策略微调权重（不改变归一化，只改变相对重要性）。
func applyStrategy(w Weights, strategy string) Weights {
	switch strings.ToUpper(strategy) {
	case "COST_FIRST":
		w.Cost *= 3
	case "SUCCESS_FIRST":
		w.Success *= 3
	}
	return w
}

// Filter 执行硬过滤：状态、币种、业务线、金额区间、卡组织、日/月限额。
func Filter(req domain.SelectionRequest, channels []domain.Channel, monthUsage map[[16]byte]int64) []domain.Channel {
	out := make([]domain.Channel, 0, len(channels))
	for _, c := range channels {
		if req.AllowedAcquirerChannels != nil && !req.AllowedAcquirerChannels[c.AcquirerChannelID] {
			continue
		}
		if !strings.EqualFold(c.Status, "ACTIVE") {
			continue
		}
		if len(c.Currencies) > 0 && !containsFold(c.Currencies, req.Currency) {
			continue
		}
		// 业务线为空时以 policy 绑定为准；非空时必须命中通道获批业务线
		if req.BusinessLine != "" && len(c.BusinessLines) > 0 && !containsFold(c.BusinessLines, req.BusinessLine) {
			continue
		}
		if c.MinAmountMinor > 0 && req.AmountMinor < c.MinAmountMinor {
			continue
		}
		if c.MaxAmountMinor > 0 && req.AmountMinor > c.MaxAmountMinor {
			continue
		}
		if req.CardBrand != "" && len(c.CardBrands) > 0 && !containsFold(c.CardBrands, req.CardBrand) {
			continue
		}
		if remaining, limited := c.RemainingDaily(); limited && remaining < req.AmountMinor {
			continue
		}
		if c.MonthlyLimitMinor > 0 {
			if monthUsage[c.ID]+req.AmountMinor > c.MonthlyLimitMinor {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// Score 计算单通道得分（0..1），再乘以通道权重得到抽样权重。
func Score(c domain.Channel, req domain.SelectionRequest, w Weights) float64 {
	success := clamp01(float64(c.HealthScore) / 10000.0)

	cost := 1 - clamp01(float64(c.CostBps)/500.0)

	capacity := 1.0
	if remaining, limited := c.RemainingDaily(); limited && c.DailyLimitMinor > 0 {
		capacity = clamp01(float64(remaining) / float64(c.DailyLimitMinor))
	}

	speed := 1 - clamp01(float64(c.SettlementDays)/7.0)

	return w.Success*success + w.Cost*cost + w.Capacity*capacity + w.Speed*speed
}

// BuildCandidates 返回按得分降序排列的候选集。
func BuildCandidates(channels []domain.Channel, req domain.SelectionRequest, weights map[string]float64, strategy string) []domain.Candidate {
	w := applyStrategy(WeightsFromMap(weights), strategy)
	candidates := make([]domain.Candidate, 0, len(channels))
	for _, c := range channels {
		score := Score(c, req, w)
		weight := float64(c.Weight) * math.Max(score, 0.0001)
		candidates = append(candidates, domain.Candidate{Channel: c, Score: score, Weight: weight})
	}
	sortCandidates(candidates)
	return candidates
}

// PickWeighted 用给定随机数 [0,1) 做加权抽样，便于确定性测试。
func PickWeighted(candidates []domain.Candidate, roll float64) *domain.Candidate {
	var total float64
	for _, c := range candidates {
		total += c.Weight
	}
	if total <= 0 {
		return nil
	}
	target := roll * total
	var acc float64
	for i := range candidates {
		acc += candidates[i].Weight
		if target < acc {
			return &candidates[i]
		}
	}
	return &candidates[len(candidates)-1]
}

func sortCandidates(c []domain.Candidate) {
	// 插入排序：候选数量很小（通常 < 20），避免引入额外依赖
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].Score > c[j-1].Score; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}

func containsFold(list []string, target string) bool {
	for _, v := range list {
		if strings.EqualFold(v, target) {
			return true
		}
	}
	return false
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
