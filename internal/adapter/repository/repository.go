package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/nexusglobal/mamr/internal/adapter/domain"
)

// AdapterRepository 适配器仓库接口定义
// 实际实现应使用 sqlc 生成的代码

// Repository 适配器仓库接口
type Repository interface {
	// GetAdapterProviderByID 获取提供商
	GetAdapterProviderByID(ctx context.Context, providerID uuid.UUID) (*domain.AdapterProvider, error)

	// GetAdapterProviderByCode 获取提供商（按代码）
	GetAdapterProviderByCode(ctx context.Context, providerCode string) (*domain.AdapterProvider, error)

	// GetAdapterChannelByID 获取通道
	GetAdapterChannelByID(ctx context.Context, channelID uuid.UUID) (*domain.AdapterChannel, error)

	// GetAdapterChannelByCode 获取通道（按代码）
	GetAdapterChannelByCode(ctx context.Context, tenantID uuid.UUID, channelCode string) (*domain.AdapterChannel, error)

	// GetAdapterChannelsByTenant 获取租户所有通道
	GetAdapterChannelsByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.AdapterChannel, error)

	// GetAdapterChannelsByType 获取指定类型的通道
	GetAdapterChannelsByType(ctx context.Context, tenantID uuid.UUID, adapterType domain.AdapterType) ([]domain.AdapterChannel, error)

	// GetAdapterChannelsByEntity 获取法人实体所有通道（V3）
	GetAdapterChannelsByEntity(ctx context.Context, entityID uuid.UUID) ([]domain.AdapterChannel, error)

	// GetAdapterCredential 获取凭证
	GetAdapterCredential(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID, credType domain.CredentialType) (*domain.AdapterCredentialVault, error)

	// DisableChannel 禁用通道
	DisableChannel(ctx context.Context, channelID uuid.UUID) error

	// EnableChannel 启用通道
	EnableChannel(ctx context.Context, channelID uuid.UUID) error

	// UpdateChannelVersion 更新通道版本
	UpdateChannelVersion(ctx context.Context, channelID uuid.UUID, version int) error
}

// AssetRepository 资产仓库接口
type AssetRepository interface {
	// GetAssetByID 获取资产
	GetAssetByID(ctx context.Context, assetID uuid.UUID) (*domain.AssetDefinition, error)

	// GetAssetByCode 获取资产（按代码）
	GetAssetByCode(ctx context.Context, assetCode string, network string) (*domain.AssetDefinition, error)

	// GetAllAssets 获取所有资产
	GetAllAssets(ctx context.Context) ([]domain.AssetDefinition, error)

	// GetActiveAssets 获取所有活跃资产
	GetActiveAssets(ctx context.Context) ([]domain.AssetDefinition, error)

	// CreateAsset 创建资产
	CreateAsset(ctx context.Context, asset *domain.AssetDefinition) error

	// UpdateAsset 更新资产
	UpdateAsset(ctx context.Context, asset *domain.AssetDefinition) error
}

// CredentialRepository 凭证仓库接口
type CredentialRepository interface {
	// GetCredential 获取凭证
	GetCredential(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID, credType domain.CredentialType) (*domain.AdapterCredentialVault, error)

	// CreateCredential 创建凭证
	CreateCredential(ctx context.Context, cred *domain.AdapterCredentialVault) error

	// UpdateCredential 更新凭证
	UpdateCredential(ctx context.Context, cred *domain.AdapterCredentialVault) error

	// RevokeCredential 撤销凭证
	RevokeCredential(ctx context.Context, credID uuid.UUID) error

	// GetActiveCredentials 获取活跃凭证
	GetActiveCredentials(ctx context.Context, providerID uuid.UUID, channelID *uuid.UUID) ([]domain.AdapterCredentialVault, error)
}
