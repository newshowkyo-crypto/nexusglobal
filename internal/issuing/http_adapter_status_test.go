package issuing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// ============================================================================
// R20 P1-3: HTTP Provider 错误分类定向 httptest 测试
// ============================================================================

var (
	testMerchantID = uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	testProviderID = uuid.MustParse("660e8400-e29b-41d4-a716-446655440001")
	testCardID     = uuid.MustParse("770e8400-e29b-41d4-a716-446655440002")
)

func TestHTTPAdapterCreateCard_408ReturnsErrProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	_, err := adapter.CreateCard(context.Background(), CreateCardRequest{
		MerchantID:     testMerchantID,
		ProviderID:     testProviderID,
		PoolCode:       "pool1",
		Currency:       "USD",
		IdempotencyKey: "idem-1",
	})
	if err == nil {
		t.Fatal("expected error for 408, got nil")
	}
	if !errors.Is(err, ErrProviderTimeout) {
		t.Errorf("expected ErrProviderTimeout for 408, got: %v", err)
	}
}

func TestHTTPAdapterCreateCard_504ReturnsErrProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	_, err := adapter.CreateCard(context.Background(), CreateCardRequest{
		MerchantID:     testMerchantID,
		ProviderID:     testProviderID,
		PoolCode:       "pool1",
		Currency:       "USD",
		IdempotencyKey: "idem-1",
	})
	if err == nil {
		t.Fatal("expected error for 504, got nil")
	}
	if !errors.Is(err, ErrProviderTimeout) {
		t.Errorf("expected ErrProviderTimeout for 504, got: %v", err)
	}
}

func TestHTTPAdapterCreateCard_429ReturnsErrProviderRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	_, err := adapter.CreateCard(context.Background(), CreateCardRequest{
		MerchantID:     testMerchantID,
		ProviderID:     testProviderID,
		PoolCode:       "pool1",
		Currency:       "USD",
		IdempotencyKey: "idem-1",
	})
	if err == nil {
		t.Fatal("expected error for 429, got nil")
	}
	if !errors.Is(err, ErrProviderRetryable) {
		t.Errorf("expected ErrProviderRetryable for 429, got: %v", err)
	}
}

func TestHTTPAdapterCreateCard_500ReturnsErrProviderRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	_, err := adapter.CreateCard(context.Background(), CreateCardRequest{
		MerchantID:     testMerchantID,
		ProviderID:     testProviderID,
		PoolCode:       "pool1",
		Currency:       "USD",
		IdempotencyKey: "idem-1",
	})
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	if !errors.Is(err, ErrProviderRetryable) {
		t.Errorf("expected ErrProviderRetryable for 500, got: %v", err)
	}
}

func TestHTTPAdapterCreateCard_400ReturnsErrProviderPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	_, err := adapter.CreateCard(context.Background(), CreateCardRequest{
		MerchantID:     testMerchantID,
		ProviderID:     testProviderID,
		PoolCode:       "pool1",
		Currency:       "USD",
		IdempotencyKey: "idem-1",
	})
	if err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if !errors.Is(err, ErrProviderPermanent) {
		t.Errorf("expected ErrProviderPermanent for 400, got: %v", err)
	}
}

func TestHTTPAdapterCreateCard_200ReturnsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"card_id":"550e8400-e29b-41d4-a716-446655440000","provider_card_ref":"ref-123","last4":"1234","status":"ACTIVE"}`))
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	result, err := adapter.CreateCard(context.Background(), CreateCardRequest{
		MerchantID:     testMerchantID,
		ProviderID:     testProviderID,
		PoolCode:       "pool1",
		Currency:       "USD",
		IdempotencyKey: "idem-1",
	})
	if err != nil {
		t.Fatalf("unexpected error for 200: %v", err)
	}
	if result.ProviderCardRef != "ref-123" {
		t.Errorf("ProviderCardRef: got %q, want %q", result.ProviderCardRef, "ref-123")
	}
}

// ============================================================================
// R20 P1-3: FreezeCard HTTP 状态码分类测试
// ============================================================================

func TestHTTPAdapterFreezeCard_200ReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	err := adapter.FreezeCard(context.Background(), FreezeCardRequest{
		ProviderCardRef: "ref-123",
		IdempotencyKey:  "idem-1",
		FrozenReason:    "fraud",
	})
	if err != nil {
		t.Errorf("expected nil for 200, got: %v", err)
	}
}

func TestHTTPAdapterFreezeCard_202ReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	err := adapter.FreezeCard(context.Background(), FreezeCardRequest{
		ProviderCardRef: "ref-123",
		IdempotencyKey:  "idem-1",
		FrozenReason:    "fraud",
	})
	if err != nil {
		t.Errorf("expected nil for 202, got: %v", err)
	}
}

func TestHTTPAdapterFreezeCard_204ReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	err := adapter.FreezeCard(context.Background(), FreezeCardRequest{
		ProviderCardRef: "ref-123",
		IdempotencyKey:  "idem-1",
		FrozenReason:    "fraud",
	})
	if err != nil {
		t.Errorf("expected nil for 204, got: %v", err)
	}
}

func TestHTTPAdapterFreezeCard_408ReturnsErrProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	err := adapter.FreezeCard(context.Background(), FreezeCardRequest{
		ProviderCardRef: "ref-123",
		IdempotencyKey:  "idem-1",
		FrozenReason:    "fraud",
	})
	if err == nil {
		t.Fatal("expected error for 408, got nil")
	}
	if !errors.Is(err, ErrProviderTimeout) {
		t.Errorf("expected ErrProviderTimeout for 408, got: %v", err)
	}
}

func TestHTTPAdapterFreezeCard_429ReturnsErrProviderRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	err := adapter.FreezeCard(context.Background(), FreezeCardRequest{
		ProviderCardRef: "ref-123",
		IdempotencyKey:  "idem-1",
		FrozenReason:    "fraud",
	})
	if err == nil {
		t.Fatal("expected error for 429, got nil")
	}
	if !errors.Is(err, ErrProviderRetryable) {
		t.Errorf("expected ErrProviderRetryable for 429, got: %v", err)
	}
}

func TestHTTPAdapterFreezeCard_400ReturnsErrProviderPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	err := adapter.FreezeCard(context.Background(), FreezeCardRequest{
		ProviderCardRef: "ref-123",
		IdempotencyKey:  "idem-1",
		FrozenReason:    "fraud",
	})
	if err == nil {
		t.Fatal("expected error for 400, got nil")
	}
	if !errors.Is(err, ErrProviderPermanent) {
		t.Errorf("expected ErrProviderPermanent for 400, got: %v", err)
	}
}

// ============================================================================
// R20 P1-3: TerminateCard HTTP 状态码分类测试
// ============================================================================

func TestHTTPAdapterTerminateCard_500ReturnsErrProviderRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	err := adapter.TerminateCard(context.Background(), TerminateCardRequest{
		CardID:           testCardID,
		ProviderID:       testProviderID,
		TerminatedReason: "expired",
	})
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	if !errors.Is(err, ErrProviderRetryable) {
		t.Errorf("expected ErrProviderRetryable for 500, got: %v", err)
	}
}

func TestHTTPAdapterTerminateCard_404ReturnsErrProviderPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	adapter := NewHTTPAdapter(srv.URL, "test-api-key")
	err := adapter.TerminateCard(context.Background(), TerminateCardRequest{
		CardID:           testCardID,
		ProviderID:       testProviderID,
		TerminatedReason: "expired",
	})
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !errors.Is(err, ErrProviderPermanent) {
		t.Errorf("expected ErrProviderPermanent for 404, got: %v", err)
	}
}
