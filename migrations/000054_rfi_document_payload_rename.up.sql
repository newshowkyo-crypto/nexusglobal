-- ADM-012: compliance_rfi_documents.pdf_content 实际存的是 JSON 元数据 payload，
-- 从未生成真实 PDF。列名改为 document_payload，消除虚假 PDF 语义。
ALTER TABLE compliance_rfi_documents RENAME COLUMN pdf_content TO document_payload;

COMMENT ON COLUMN compliance_rfi_documents.document_payload IS 'RFI 文档 payload（JSON 元数据），非 PDF 二进制';
