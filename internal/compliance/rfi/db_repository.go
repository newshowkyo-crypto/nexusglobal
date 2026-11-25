package rfi

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type dbRepository struct {
	pool *pgxpool.Pool
}

func NewDBRepository(pool *pgxpool.Pool) Repository {
	return &dbRepository{pool: pool}
}

func (r *dbRepository) CreateDocument(ctx context.Context, doc *RFIDocument) error {
	metadataJSON, err := json.Marshal(doc.Metadata)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO compliance_rfi_documents
		    (id, order_id, merchant_id, document_type, source_data_hash, status, document_payload, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		doc.ID, doc.OrderID, doc.MerchantID, doc.DocumentType, doc.SourceDataHash,
		doc.Status, doc.DocumentPayload, metadataJSON, doc.CreatedAt, doc.UpdatedAt)
	return err
}

func (r *dbRepository) GetDocumentByID(ctx context.Context, id uuid.UUID) (*RFIDocument, error) {
	var doc RFIDocument
	var metadataJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, order_id, merchant_id, document_type, source_data_hash, status,
		       document_payload, metadata, approved_by, approved_at, rejected_by, rejected_at, rejected_reason, created_at, updated_at
		FROM compliance_rfi_documents WHERE id = $1`, id).Scan(
		&doc.ID, &doc.OrderID, &doc.MerchantID, &doc.DocumentType, &doc.SourceDataHash, &doc.Status,
		&doc.DocumentPayload, &metadataJSON, &doc.ApprovedBy, &doc.ApprovedAt, &doc.RejectedBy, &doc.RejectedAt, &doc.RejectedReason,
		&doc.CreatedAt, &doc.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDocumentNotFound
	}
	if err != nil {
		return nil, err
	}
	if metadataJSON != nil {
		_ = json.Unmarshal(metadataJSON, &doc.Metadata)
	}
	return &doc, nil
}

func (r *dbRepository) ListDocuments(ctx context.Context, merchantID *uuid.UUID, status *DocumentStatus) ([]*RFIDocument, error) {
	query := `SELECT id, order_id, merchant_id, document_type, source_data_hash, status,
	                 document_payload, metadata, approved_by, approved_at, rejected_by, rejected_at, rejected_reason, created_at, updated_at
	          FROM compliance_rfi_documents WHERE 1=1`
	args := []interface{}{}
	if merchantID != nil {
		args = append(args, *merchantID)
		query += ` AND merchant_id = $` + pgxArg(len(args))
	}
	if status != nil {
		args = append(args, *status)
		query += ` AND status = $` + pgxArg(len(args))
	}
	query += ` ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []*RFIDocument
	for rows.Next() {
		var doc RFIDocument
		var metadataJSON []byte
		if err := rows.Scan(
			&doc.ID, &doc.OrderID, &doc.MerchantID, &doc.DocumentType, &doc.SourceDataHash, &doc.Status,
			&doc.DocumentPayload, &metadataJSON, &doc.ApprovedBy, &doc.ApprovedAt, &doc.RejectedBy, &doc.RejectedAt, &doc.RejectedReason,
			&doc.CreatedAt, &doc.UpdatedAt); err != nil {
			return nil, err
		}
		if metadataJSON != nil {
			_ = json.Unmarshal(metadataJSON, &doc.Metadata)
		}
		docs = append(docs, &doc)
	}
	return docs, rows.Err()
}

func (r *dbRepository) UpdateDocumentStatus(ctx context.Context, id uuid.UUID, status DocumentStatus, operator string, reason *string) error {
	now := time.Now()
	switch status {
	case DocumentStatusApproved:
		_, err := r.pool.Exec(ctx, `
			UPDATE compliance_rfi_documents
			SET status = $2, approved_by = $3, approved_at = $4, updated_at = $4 WHERE id = $1`,
			id, status, operator, now)
		return err
	case DocumentStatusRejected:
		var reasonStr string
		if reason != nil {
			reasonStr = *reason
		}
		_, err := r.pool.Exec(ctx, `
			UPDATE compliance_rfi_documents
			SET status = $2, rejected_by = $3, rejected_at = $4, rejected_reason = $5, updated_at = $4 WHERE id = $1`,
			id, status, operator, now, reasonStr)
		return err
	default:
		_, err := r.pool.Exec(ctx, `
			UPDATE compliance_rfi_documents SET status = $2, updated_at = $3 WHERE id = $1`,
			id, status, now)
		return err
	}
}

func pgxArg(n int) string {
	return string(rune('0' + n))
}
