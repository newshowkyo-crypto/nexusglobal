package token

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const tokenPrefix = "token:"
const tokenTTL = 24 * time.Hour // Redis key TTL，比 token 自身 TTL 稍长

type RedisStore struct {
	rdb *redis.Client
}

func NewRedisStore(rdb *redis.Client) *RedisStore {
	return &RedisStore{rdb: rdb}
}

func (s *RedisStore) Save(ctx context.Context, token *Token) error {
	data, err := json.Marshal(tokenData{
		ID:            token.ID,
		Value:         token.Value,
		OrderID:       token.OrderID,
		MerchantID:    token.MerchantID,
		AllowedOrigin: token.AllowedOrigin,
		MaxUses:       token.MaxUses,
		UsedCount:     token.UsedCount,
		ExpiresAt:     token.ExpiresAt.Unix(),
		CreatedAt:     token.CreatedAt.Unix(),
	})
	if err != nil {
		return fmt.Errorf("token: marshal: %w", err)
	}

	key := tokenPrefix + token.Value
	ttl := time.Until(token.ExpiresAt)
	if ttl <= 0 {
		ttl = tokenTTL
	}

	return s.rdb.Set(ctx, key, data, ttl).Err()
}

func (s *RedisStore) Get(ctx context.Context, value string) (*Token, error) {
	key := tokenPrefix + value
	data, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}

	var td tokenData
	if err := json.Unmarshal(data, &td); err != nil {
		return nil, fmt.Errorf("token: unmarshal: %w", err)
	}

	return &Token{
		ID:            td.ID,
		Value:         td.Value,
		OrderID:       td.OrderID,
		MerchantID:    td.MerchantID,
		AllowedOrigin: td.AllowedOrigin,
		MaxUses:       td.MaxUses,
		UsedCount:     td.UsedCount,
		ExpiresAt:     time.Unix(td.ExpiresAt, 0),
		CreatedAt:     time.Unix(td.CreatedAt, 0),
	}, nil
}

// IncrementUseCount 原子递增使用次数
// 使用 Lua 脚本保证"检查+递增"原子性
func (s *RedisStore) IncrementUseCount(ctx context.Context, value string) error {
	key := tokenPrefix + value

	// Lua 脚本：检查是否存在，存在则递增
	script := `
		local key = KEYS[1]
		local data = redis.call('GET', key)
		if not data then
			return -1
		end
		local cjson = cjson
		local token = cjson.decode(data)
		token.used_count = token.used_count + 1
		local ttl = redis.call('TTL', key)
		redis.call('SET', key, cjson.encode(token), 'EX', ttl)
		return token.used_count
	`

	result, err := s.rdb.Eval(ctx, script, []string{key}).Int64()
	if err != nil {
		return fmt.Errorf("token: increment: %w", err)
	}
	if result < 0 {
		return ErrTokenNotFound
	}

	return nil
}

type tokenData struct {
	ID            string `json:"id"`
	Value         string `json:"value"`
	OrderID       string `json:"order_id"`
	MerchantID    string `json:"merchant_id"`
	AllowedOrigin string `json:"allowed_origin"`
	MaxUses       int    `json:"max_uses"`
	UsedCount     int    `json:"used_count"`
	ExpiresAt     int64  `json:"expires_at"`
	CreatedAt     int64  `json:"created_at"`
}
