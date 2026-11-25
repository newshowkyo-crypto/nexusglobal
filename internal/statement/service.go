package statement

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Service 对账单生成与导出。
type Service struct {
	repo       *Repository
	signingKey []byte
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// SetSigningKey 注入签名密钥（P2：签名下载）。
// 未注入时不提供签名下载能力（fail-closed，不生成可公开访问的链接）。
func (s *Service) SetSigningKey(key []byte) { s.signingKey = key }

// SigningEnabled 返回是否已配置签名密钥。
func (s *Service) SigningEnabled() bool { return len(s.signingKey) > 0 }

// GenerateAll 为指定营业日的全部活跃 (merchant, currency) 生成对账单。
func (s *Service) GenerateAll(ctx context.Context, day time.Time) ([]Statement, error) {
	activities, err := s.repo.ListActivity(ctx, day)
	if err != nil {
		return nil, err
	}
	out := make([]Statement, 0, len(activities))
	for _, a := range activities {
		st, err := s.repo.Generate(ctx, a.MerchantID, a.Currency, day)
		if err != nil {
			return out, err
		}
		out = append(out, *st)
	}
	return out, nil
}

// Generate 为单个商户+币种生成对账单。
func (s *Service) Generate(ctx context.Context, merchantID uuid.UUID, currency string, day time.Time) (*Statement, error) {
	if currency == "" {
		return nil, fmt.Errorf("statement: currency is required")
	}
	return s.repo.Generate(ctx, merchantID, currency, day)
}

// Get 查询对账单。
func (s *Service) Get(ctx context.Context, merchantID, id uuid.UUID) (*Statement, error) {
	return s.repo.Get(ctx, merchantID, id)
}

// List 查询对账单列表。
func (s *Service) List(ctx context.Context, merchantID uuid.UUID, day string, limit int) ([]Statement, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repo.List(ctx, merchantID, day, limit)
}

// CSV 导出对账单（UTF-8 CSV，含表头）。
func (s *Service) CSV(ctx context.Context, merchantID, id uuid.UUID) ([]byte, string, error) {
	st, err := s.repo.Get(ctx, merchantID, id)
	if err != nil {
		return nil, "", err
	}
	if st == nil {
		return nil, "", ErrNotFound
	}
	lines, err := s.repo.Lines(ctx, id)
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	header := []string{
		"statement_id", "merchant_id", "currency", "business_date",
		"line_type", "reference_id", "merchant_order_id", "amount_minor", "occurred_at", "description",
	}
	if err := w.Write(header); err != nil {
		return nil, "", err
	}
	for _, l := range lines {
		row := []string{
			st.ID.String(), st.MerchantID.String(), st.Currency, st.BusinessDate,
			l.LineType, l.ReferenceID.String(), l.MerchantOrderID,
			strconv.FormatInt(l.AmountMinor, 10), l.OccurredAt.UTC().Format(time.RFC3339), l.Description,
		}
		if err := w.Write(row); err != nil {
			return nil, "", err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, "", err
	}

	filename := fmt.Sprintf("statement_%s_%s_%s.csv", st.MerchantID, st.BusinessDate, st.Currency)
	return buf.Bytes(), filename, nil
}

// ErrNotFound 对账单不存在。
var ErrNotFound = errors.New("statement: not found")

// ErrSignatureInvalid 下载签名无效或已过期。
var ErrSignatureInvalid = errors.New("statement: download signature invalid or expired")

// ErrSigningDisabled 未配置签名密钥。
var ErrSigningDisabled = errors.New("statement: signing key not configured")

// DownloadLink 签名下载链接。
type DownloadLink struct {
	URL       string `json:"url"`
	ExpiresAt int64  `json:"expires_at"`
	Signature string `json:"signature"`
}

const downloadSignatureVersion = "v1"

// downloadPayload 签名原文：版本 + 商户 + 对账单 + 过期时间。
func downloadPayload(merchantID, statementID uuid.UUID, expires int64) string {
	return fmt.Sprintf("%s\n%s\n%s\n%d", downloadSignatureVersion, merchantID, statementID, expires)
}

func (s *Service) sign(payload string) string {
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignDownload 为对账单生成带时效的下载链接。
func (s *Service) SignDownload(ctx context.Context, merchantID, id uuid.UUID, ttl time.Duration) (*DownloadLink, error) {
	if !s.SigningEnabled() {
		return nil, ErrSigningDisabled
	}
	if ttl <= 0 || ttl > 7*24*time.Hour {
		ttl = time.Hour
	}
	st, err := s.repo.Get(ctx, merchantID, id)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, ErrNotFound
	}

	expires := time.Now().Add(ttl).Unix()
	signature := s.sign(downloadPayload(merchantID, id, expires))
	return &DownloadLink{
		URL:       fmt.Sprintf("/public/statements/%s/download?expires=%d&signature=%s", id, expires, signature),
		ExpiresAt: expires,
		Signature: signature,
	}, nil
}

// VerifyDownload 校验签名下载链接：存在性、时效、商户归属、签名。
func (s *Service) VerifyDownload(ctx context.Context, id uuid.UUID, expires int64, signature string) (uuid.UUID, error) {
	if !s.SigningEnabled() {
		return uuid.Nil, ErrSigningDisabled
	}
	if signature == "" || expires <= 0 {
		return uuid.Nil, ErrSignatureInvalid
	}
	if time.Now().Unix() > expires {
		return uuid.Nil, ErrSignatureInvalid
	}

	st, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return uuid.Nil, err
	}
	if st == nil {
		return uuid.Nil, ErrNotFound
	}

	expected := s.sign(downloadPayload(st.MerchantID, id, expires))
	if !hmac.Equal([]byte(expected), []byte(strings.ToLower(signature))) {
		return uuid.Nil, ErrSignatureInvalid
	}
	return st.MerchantID, nil
}

// SignBody 对 CSV 内容签名（附在响应头，供接收方校验完整性）。
func (s *Service) SignBody(data []byte) string {
	if !s.SigningEnabled() {
		return ""
	}
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// Lines 返回对账单明细。
func (s *Service) Lines(ctx context.Context, id uuid.UUID) ([]Line, error) {
	return s.repo.Lines(ctx, id)
}
