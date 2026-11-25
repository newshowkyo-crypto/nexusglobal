package pool

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/pool/domain"
)

// Redis 水位是"快速拒绝"缓存：DB 仍是唯一真相。
// 缓存命中且已满时直接跳过该通道，避免无谓的 DB 原子更新；
// 缓存未命中或 Redis 故障时一律回落到 DB 判定，不改变正确性。

const reserveScript = `
local used = tonumber(redis.call('HGET', KEYS[1], 'used') or '0')
local reserved = tonumber(redis.call('HGET', KEYS[1], 'reserved') or '0')
local amount = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
if limit > 0 and used + reserved + amount > limit then
  return 0
end
redis.call('HINCRBY', KEYS[1], 'reserved', amount)
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[3]))
return 1
`

const releaseScript = `
local reserved = tonumber(redis.call('HGET', KEYS[1], 'reserved') or '0')
local amount = tonumber(ARGV[1])
reserved = reserved - amount
if reserved < 0 then reserved = 0 end
redis.call('HSET', KEYS[1], 'reserved', reserved)
return 1
`

const confirmScript = `
local reserved = tonumber(redis.call('HGET', KEYS[1], 'reserved') or '0')
local amount = tonumber(ARGV[1])
reserved = reserved - amount
if reserved < 0 then reserved = 0 end
redis.call('HSET', KEYS[1], 'reserved', reserved)
redis.call('HINCRBY', KEYS[1], 'used', amount)
return 1
`

func watermarkKey(channelID uuid.UUID, day time.Time) string {
	return "pool:wm:" + channelID.String() + ":" + day.Format("20060102")
}

func ttlSeconds(now time.Time) int {
	end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location()).Add(2 * time.Hour)
	sec := int(end.Sub(now).Seconds())
	if sec < 60 {
		sec = 60
	}
	return sec
}

// tryReserveRedis 返回 1=预占成功，0=已满，-1=Redis 不可用（调用方回落 DB）。
func (s *Service) tryReserveRedis(ctx context.Context, channelID uuid.UUID, day time.Time, amount, limit int64, wm domain.Watermark) int {
	if s.rdb == nil {
		return -1
	}
	key := watermarkKey(channelID, day)
	// 冷启动：用 DB 快照做种子，避免 Redis 从 0 开始低估用量
	_ = s.rdb.HSetNX(ctx, key, "used", wm.UsedAmountMinor).Err()
	_ = s.rdb.HSetNX(ctx, key, "reserved", wm.ReservedAmountMinor).Err()

	res, err := s.rdb.Eval(ctx, reserveScript, []string{key}, amount, limit, ttlSeconds(s.now())).Int()
	if err != nil {
		return -1
	}
	return res
}

func (s *Service) releaseRedis(ctx context.Context, channelID uuid.UUID, day time.Time, amount int64) {
	if s.rdb == nil {
		return
	}
	_ = s.rdb.Eval(ctx, releaseScript, []string{watermarkKey(channelID, day)}, amount).Err()
}

func (s *Service) confirmRedis(ctx context.Context, channelID uuid.UUID, day time.Time, amount int64) {
	if s.rdb == nil {
		return
	}
	_ = s.rdb.Eval(ctx, confirmScript, []string{watermarkKey(channelID, day)}, amount).Err()
}
