package token

import (
	"context"
	"sync"
)

type MemoryStore struct {
	mu     sync.RWMutex
	tokens map[string]*Token
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tokens: make(map[string]*Token),
	}
}

func (s *MemoryStore) Save(_ context.Context, token *Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := *token
	s.tokens[token.Value] = &t
	return nil
}

func (s *MemoryStore) Get(_ context.Context, value string) (*Token, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tokens[value]
	if !ok {
		return nil, ErrTokenNotFound
	}
	result := *t
	return &result, nil
}

func (s *MemoryStore) IncrementUseCount(_ context.Context, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[value]
	if !ok {
		return ErrTokenNotFound
	}
	t.UsedCount++
	return nil
}
