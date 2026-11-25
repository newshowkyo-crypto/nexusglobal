package pool

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/nexusglobal/mamr/internal/pool/domain"
)

const (
	leaseTTL       = 30 * time.Minute
	maxStandbyRank = 4
)

// Service 支付池分配器。
type Service struct {
	store  Store
	mode   domain.Mode
	logger *slog.Logger
	now    func() time.Time
	// roll 返回 [0,1) 随机数，测试可注入。
	roll func() float64
	// rdb 可选的快速拒绝缓存（DB 仍是真相）
	rdb redisClient
}

// redisClient 只暴露水位缓存需要的最小能力，便于测试与解耦。
type redisClient interface {
	HSetNX(ctx context.Context, key, field string, value interface{}) *redis.BoolCmd
	Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd
}

func New(store Store, mode domain.Mode, logger *slog.Logger) *Service {
	return &Service{
		store:  store,
		mode:   mode,
		logger: logger,
		now:    time.Now,
		roll:   rand.Float64,
	}
}

// SetRedis 注入水位快速缓存（可选）。DB 仍是唯一真相。
func (s *Service) SetRedis(rdb redisClient) { s.rdb = rdb }

// Mode 返回当前运行模式。
func (s *Service) Mode() domain.Mode { return s.mode }

// SetRollForTest 注入确定性随机源。
func (s *Service) SetRollForTest(f func() float64) { s.roll = f }

// Select 执行硬过滤 → 打分 → 加权抽样 → 额度预占 → 写租约。
func (s *Service) Select(ctx context.Context, req domain.SelectionRequest) (*domain.Decision, error) {
	if s.mode == domain.ModeOff {
		return nil, domain.ErrNoPolicy
	}
	if req.AmountMinor <= 0 {
		return nil, fmt.Errorf("pool: amount must be positive")
	}

	policy, err := s.store.GetPolicy(ctx, req.MerchantID, req.SiteID, req.BusinessLine)
	if err != nil {
		return nil, err
	}
	if policy == nil || len(policy.AllowedChannelIDs) == 0 {
		return nil, domain.ErrNoPolicy
	}

	channels, err := s.store.ListChannels(ctx, policy.AllowedChannelIDs)
	if err != nil {
		return nil, err
	}
	if len(channels) == 0 {
		return nil, domain.ErrNoCandidate
	}

	monthUsage := make(map[[16]byte]int64, len(channels))
	for _, c := range channels {
		if c.MonthlyLimitMinor > 0 {
			used, err := s.store.MonthUsage(ctx, c.ID, s.now())
			if err != nil {
				return nil, err
			}
			monthUsage[c.ID] = used
		}
	}

	eligible := Filter(req, channels, monthUsage)
	if len(eligible) == 0 {
		return nil, domain.ErrNoCandidate
	}

	candidates := BuildCandidates(eligible, req, policy.Weights, policy.Strategy)
	if len(candidates) == 0 {
		return nil, domain.ErrNoCandidate
	}

	// 从高分到低分尝试预占额度；并发下某个通道可能刚好被占满，继续尝试下一个。
	order := append([]domain.Candidate(nil), candidates...)
	for attempts := 0; attempts < len(candidates); attempts++ {
		pick := PickWeighted(order, s.roll())
		if pick == nil {
			break
		}
		now := s.now()

		// Redis 快速拒绝：明确已满则直接跳过（DB 仍是真相）
		if s.tryReserveRedis(ctx, pick.Channel.ID, now, req.AmountMinor,
			pick.Channel.DailyLimitMinor, pick.Channel.Watermark) == 0 {
			order = removeCandidate(order, pick.Channel.ID)
			continue
		}

		ok, err := s.store.ReserveWatermark(ctx, pick.Channel.ID, now, req.AmountMinor, pick.Channel.DailyLimitMinor)
		if err != nil {
			s.releaseRedis(ctx, pick.Channel.ID, now, req.AmountMinor)
			return nil, err
		}
		if !ok {
			s.releaseRedis(ctx, pick.Channel.ID, now, req.AmountMinor)
			order = removeCandidate(order, pick.Channel.ID)
			continue
		}

		monthOK, err := s.store.ReserveMonth(ctx, pick.Channel.ID, now, req.AmountMinor, pick.Channel.MonthlyLimitMinor)
		if err != nil {
			_ = s.store.ReleaseWatermark(ctx, pick.Channel.ID, now, req.AmountMinor)
			s.releaseRedis(ctx, pick.Channel.ID, now, req.AmountMinor)
			return nil, err
		}
		if !monthOK {
			_ = s.store.ReleaseWatermark(ctx, pick.Channel.ID, now, req.AmountMinor)
			s.releaseRedis(ctx, pick.Channel.ID, now, req.AmountMinor)
			order = removeCandidate(order, pick.Channel.ID)
			continue
		}

		decision, err := s.persistDecision(ctx, req, policy, pick, candidates)
		if err != nil {
			// 租约写入失败必须回滚额度，否则会永久占用
			_ = s.store.ReleaseWatermark(ctx, pick.Channel.ID, now, req.AmountMinor)
			s.releaseRedis(ctx, pick.Channel.ID, now, req.AmountMinor)
			return nil, err
		}
		return decision, nil
	}
	return nil, domain.ErrLimitExceeded
}

func (s *Service) persistDecision(ctx context.Context, req domain.SelectionRequest, policy *domain.Policy,
	chosen *domain.Candidate, all []domain.Candidate) (*domain.Decision, error) {
	if req.AttemptID != uuid.Nil {
		leases := make([]domain.Lease, 0, maxStandbyRank)
		leases = append(leases, domain.Lease{
			ID:          uuid.New(),
			ChannelID:   chosen.Channel.ID,
			AttemptID:   req.AttemptID,
			AmountMinor: req.AmountMinor,
			Rank:        1,
			State:       domain.LeaseHeld,
			ExpiresAt:   s.now().Add(leaseTTL),
		})
		rank := 2
		for _, c := range all {
			if rank > maxStandbyRank {
				break
			}
			if c.Channel.ID == chosen.Channel.ID {
				continue
			}
			leases = append(leases, domain.Lease{
				ID:          uuid.New(),
				ChannelID:   c.Channel.ID,
				AttemptID:   req.AttemptID,
				AmountMinor: req.AmountMinor,
				Rank:        rank,
				State:       domain.LeaseStandby,
				ExpiresAt:   s.now().Add(leaseTTL),
			})
			rank++
		}
		if err := s.store.InsertLeases(ctx, leases); err != nil {
			return nil, err
		}
	}

	return &domain.Decision{
		ChannelID:         chosen.Channel.ID,
		AcquirerChannelID: chosen.Channel.AcquirerChannelID,
		Score:             chosen.Score,
		Mode:              s.mode,
		Reason:            fmt.Sprintf("policy=%s/%s strategy=%s", policy.SiteID, policy.BusinessLine, policy.Strategy),
		BusinessLine:      policy.BusinessLine,
	}, nil
}

// Failover 取下一备用通道，并为它预占额度后置为 HELD。
// 备用通道在 Select 时只建了租约、没有占额度，切换时必须补占，否则会绕过限额。
func (s *Service) Failover(ctx context.Context, attemptID uuid.UUID) (*domain.Decision, error) {
	lease, err := s.store.NextStandby(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	channel, err := s.store.GetChannel(ctx, lease.ChannelID)
	if err != nil {
		return nil, err
	}
	if channel == nil {
		return nil, domain.ErrNoCandidate
	}

	now := s.now()
	if s.tryReserveRedis(ctx, channel.ID, now, lease.AmountMinor, channel.DailyLimitMinor, channel.Watermark) == 0 {
		return nil, domain.ErrLimitExceeded
	}
	ok, err := s.store.ReserveWatermark(ctx, channel.ID, now, lease.AmountMinor, channel.DailyLimitMinor)
	if err != nil {
		s.releaseRedis(ctx, channel.ID, now, lease.AmountMinor)
		return nil, err
	}
	if !ok {
		s.releaseRedis(ctx, channel.ID, now, lease.AmountMinor)
		return nil, domain.ErrLimitExceeded
	}
	monthOK, err := s.store.ReserveMonth(ctx, channel.ID, now, lease.AmountMinor, channel.MonthlyLimitMinor)
	if err != nil {
		_ = s.store.ReleaseWatermark(ctx, channel.ID, now, lease.AmountMinor)
		s.releaseRedis(ctx, channel.ID, now, lease.AmountMinor)
		return nil, err
	}
	if !monthOK {
		_ = s.store.ReleaseWatermark(ctx, channel.ID, now, lease.AmountMinor)
		s.releaseRedis(ctx, channel.ID, now, lease.AmountMinor)
		return nil, domain.ErrLimitExceeded
	}

	if err := s.store.SetLeaseState(ctx, attemptID, lease.ChannelID, domain.LeaseHeld); err != nil {
		_ = s.store.ReleaseWatermark(ctx, channel.ID, now, lease.AmountMinor)
		s.releaseRedis(ctx, channel.ID, now, lease.AmountMinor)
		return nil, err
	}
	return &domain.Decision{
		ChannelID:         channel.ID,
		AcquirerChannelID: channel.AcquirerChannelID,
		Mode:              s.mode,
		Reason:            fmt.Sprintf("failover rank=%d", lease.Rank),
	}, nil
}

// Confirm 标记当前租约为成功，并把在途额度转为已用。
func (s *Service) Confirm(ctx context.Context, attemptID, channelID uuid.UUID, amountMinor int64) error {
	if err := s.store.SetLeaseState(ctx, attemptID, channelID, domain.LeaseConfirmed); err != nil {
		return err
	}
	now := s.now()
	if err := s.store.ConfirmWatermark(ctx, channelID, now, amountMinor); err != nil {
		return err
	}
	s.confirmRedis(ctx, channelID, now, amountMinor)
	return nil
}

// Release 标记当前租约失败，并释放在途额度。
func (s *Service) Release(ctx context.Context, attemptID, channelID uuid.UUID, amountMinor int64) error {
	if err := s.store.SetLeaseState(ctx, attemptID, channelID, domain.LeaseReleased); err != nil {
		return err
	}
	now := s.now()
	if err := s.store.ReleaseWatermark(ctx, channelID, now, amountMinor); err != nil {
		return err
	}
	s.releaseRedis(ctx, channelID, now, amountMinor)
	return nil
}

func removeCandidate(list []domain.Candidate, id uuid.UUID) []domain.Candidate {
	out := list[:0]
	for _, c := range list {
		if c.Channel.ID != id {
			out = append(out, c)
		}
	}
	return out
}
