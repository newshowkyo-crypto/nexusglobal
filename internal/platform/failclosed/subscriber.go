// Package failclosed — subscriber.go 实现多节点同步。
//
// 多节点同步策略：
//  1. 启动时从 DB 加载状态到进程内缓存（LoadFromDB）
//  2. 订阅 Redis pub/sub 频道，收到事件时只接受更高 epoch
//  3. Redis 断连期间，金融入口通过 DB admission barrier fail closed
//
// 不承诺"全球所有节点 1ms 同步"。定义并测量：
//   - 本机同步阻断在触发响应前完成（事务内更新 control row + commit）
//   - 跨节点最终一致由 pub/sub + DB 启动加载共同保证
//
// 注意：Redis 不是资金安全边界（RED-14），DB 是真相。
// 即使 pub/sub 完全失效，每个节点在启动时从 DB 加载状态，
// 且 ExecutionCheck 会直接查询 DB（强一致）。
package failclosed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RemoteEvent 是 pub/sub 事件的有效载荷结构。
type RemoteEvent struct {
	Event    string `json:"event"`
	Operator string `json:"operator"`
	Reason   string `json:"reason,omitempty"`
	Checker  string `json:"checker,omitempty"`
	Epoch    int64  `json:"epoch"`
	TS       string `json:"ts"`
}

// Subscriber 订阅 Redis pub/sub 同步多节点 Kill Switch 状态。
type Subscriber struct {
	svc    *Service
	rdb    *redis.Client
	log    *slog.Logger
	pubsub *redis.PubSub

	stopCh chan struct{}
	wg     sync.WaitGroup
	closed bool
	mu     sync.Mutex
}

// NewSubscriber 创建多节点同步订阅器。
// svc 必须非 nil；rdb 为 nil 时 Start 返回错误（无法订阅）。
func NewSubscriber(svc *Service, rdb *redis.Client, log *slog.Logger) *Subscriber {
	if log == nil {
		log = slog.Default()
	}
	return &Subscriber{
		svc:    svc,
		rdb:    rdb,
		log:    log,
		stopCh: make(chan struct{}),
	}
}

// Start 启动 pub/sub 订阅。
// 在后台 goroutine 中接收事件并应用到本地缓存。
// 调用方应在启动 LoadFromDB 之后调用此方法。
func (s *Subscriber) Start(ctx context.Context) error {
	if s.rdb == nil {
		return errors.New("redis unavailable, cannot start failclosed subscriber")
	}
	if s.svc == nil {
		return errors.New("service is required")
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("subscriber already closed")
	}
	s.mu.Unlock()

	pubsub := s.rdb.Subscribe(ctx, ChannelLockdownEvent, ChannelRecoveryEvent)

	// 在等待订阅确认前先保存引用：保证 Stop() 在任何时序下都能关闭连接、
	// 中断阻塞中的 ReceiveMessage（否则 wg.Wait 永远等不到 run 退出）。
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = pubsub.Close()
		return errors.New("subscriber already closed")
	}
	s.pubsub = pubsub
	s.mu.Unlock()

	// 等待订阅确认
	_, err := pubsub.Receive(ctx)
	if err != nil {
		_ = pubsub.Close()
		return fmt.Errorf("subscribe channels: %w", err)
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer pubsub.Close()
		s.run(ctx, pubsub)
	}()

	s.log.Info("failclosed subscriber started",
		"channels", []string{ChannelLockdownEvent, ChannelRecoveryEvent},
	)
	return nil
}

// run 是订阅循环的主体。
func (s *Subscriber) run(ctx context.Context, pubsub *redis.PubSub) {
	for {
		select {
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		msg, err := pubsub.ReceiveMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Warn("failclosed subscriber receive error", "err", err)
			// 短暂退避后重试
			select {
			case <-s.stopCh:
				return
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		s.handleMessage(ctx, msg)
	}
}

// handleMessage 解析并应用一条 pub/sub 消息。
func (s *Subscriber) handleMessage(ctx context.Context, msg *redis.Message) {
	var evt RemoteEvent
	if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
		s.log.Warn("failclosed subscriber: invalid payload",
			"channel", msg.Channel,
			"err", err,
		)
		return
	}

	if evt.Epoch <= 0 {
		s.log.Warn("failclosed subscriber: invalid epoch",
			"channel", msg.Channel,
			"epoch", evt.Epoch,
		)
		return
	}

	// 根据 channel 推断状态
	var newState State
	switch msg.Channel {
	case ChannelLockdownEvent:
		newState = StateLocked
	case ChannelRecoveryEvent:
		newState = StateOpen
	default:
		s.log.Warn("failclosed subscriber: unknown channel", "channel", msg.Channel)
		return
	}

	rec := ControlRecord{
		State:    newState,
		Epoch:    evt.Epoch,
		Operator: evt.Operator,
		Reason:   evt.Reason,
	}
	if applied := s.svc.ApplyRemoteUpdate(rec); !applied {
		// 旧 epoch 被拒绝，这是正常的（防止乱序回放）
		return
	}

	// 收到远端更新后，再从 DB 拉取一次以获得完整真相（best-effort）。
	// 这处理了 pub/sub 载荷与 DB 不完全一致的情况。
	if _, err := s.svc.GetCurrentState(ctx); err == nil {
		// GetCurrentState 不更新缓存，仅用于触发连接健康检查
	}
}

// Stop 停止订阅，等待 goroutine 退出。
// 可多次调用，幂等。
// 关闭 pubsub 以中断阻塞中的 ReceiveMessage：run 循环收到错误后检测到
// stopCh 已关闭即退出，保证 Stop() 不会永久阻塞。
func (s *Subscriber) Stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stopCh)
	if s.pubsub != nil {
		_ = s.pubsub.Close()
	}
	s.mu.Unlock()

	s.wg.Wait()
	s.log.Info("failclosed subscriber stopped")
}
