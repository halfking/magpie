package core

import (
	"context"
	"time"
)

// BalanceProvider 定义余额提供者接口
// 每个 provider（如 DeepSeek、Moonshot 等）需要实现此接口
type BalanceProvider interface {
	// Name 返回 provider 的唯一标识名称
	Name() string

	// Hosts 返回该 provider 支持的 API host 列表
	// 例如: ["api.deepseek.com"]
	Hosts() []string

	// FetchBalance 获取余额信息
	// 返回 BalanceResult 包含完整的余额数据
	FetchBalance(ctx context.Context, config ProviderConfig) (*BalanceResult, error)

	// ParseResponse 解析 API 响应体
	// 用于将 HTTP 响应转换为结构化的余额数据
	ParseResponse(body []byte) (*BalanceResult, error)

	// IsDeprecated 标识该 provider 的 API 是否已废弃
	// 例如 SiliconFlow 在 2026-08-14 废弃了余额查询接口
	IsDeprecated() bool

	// DeprecationMessage 返回废弃说明（如果已废弃）
	DeprecationMessage() string
}

// ProviderConfig 包含 provider 的配置信息
type ProviderConfig struct {
	// 基础信息
	ProviderID   string // Provider ID
	APIKey       string // API Key
	BalanceToken string // 账户级别的 token（可选）

	// 自定义配置
	BalanceURL    string            // 自定义余额查询 URL
	BalancePath   string            // 自定义 JSONPath
	BalanceMethod string            // HTTP 方法（GET/POST）
	Headers       map[string]string // 自定义 Headers

	// 请求配置
	Timeout     time.Duration // 请求超时时间
	EnableRetry bool          // 是否启用重试

	// 内部使用
	ChatEndpoint      string // Chat 端点（用于识别 host）
	ResponsesEndpoint string // Responses 端点
	AnthropicEndpoint string // Anthropic 端点
}

// BalanceResult 表示余额查询的完整结果
type BalanceResult struct {
	// 显示信息
	Amount   string        `json:"amount"`   // 主显示金额（如 "¥12.34"）
	Parts    []BalancePart `json:"parts"`    // 金额组成部分（用于多部分显示）
	Split    []BalancePart `json:"split"`    // 余额拆分（如赠送/充值）
	Currency string        `json:"currency"` // 币种代码（CNY/USD/等）

	// 限额窗口（用于限流控制）
	Windows []QuotaWindow `json:"windows,omitempty"`

	// 元数据
	IsShared  bool      `json:"is_shared"` // 是否为账户级共享余额
	Timestamp time.Time `json:"timestamp"` // 查询时间
	CacheKey  string    `json:"cache_key"` // 缓存键

	// 原始响应（调试用）
	RawResponse []byte `json:"-"`
}

// BalancePart 表示余额的一个组成部分
type BalancePart struct {
	Label   string     `json:"label,omitempty"`   // 标签（如 "Granted", "Topped up"）
	Text    string     `json:"text"`              // 显示文本
	Percent *float64   `json:"percent,omitempty"` // 百分比（0-100）
	Error   string     `json:"error,omitempty"`   // 错误信息
	Expires *time.Time `json:"expires,omitempty"` // 过期时间
}

// QuotaWindow 表示配额限制窗口
type QuotaWindow struct {
	Type   string     `json:"type"`            // 窗口类型（如 "5h", "week", "month"）
	Limit  float64    `json:"limit"`           // 限额上限
	Amount float64    `json:"amount"`          // 已使用量
	Reset  *time.Time `json:"reset,omitempty"` // 重置时间
}

// BalanceCache 定义缓存接口
type BalanceCache interface {
	// Get 获取缓存的余额数据
	Get(key string) (*BalanceResult, bool)

	// Set 设置缓存
	Set(key string, result *BalanceResult, ttl time.Duration)

	// Delete 删除缓存
	Delete(key string)

	// Clear 清空所有缓存
	Clear()

	// Stats 返回缓存统计信息
	Stats() CacheStats
}

// CacheStats 缓存统计信息
type CacheStats struct {
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Evictions int64 `json:"evictions"`
	Size      int   `json:"size"`
	MaxSize   int   `json:"max_size"`
}

// BalanceMetrics 余额查询的监控指标
type BalanceMetrics struct {
	TotalRequests   int64         `json:"total_requests"`
	SuccessRequests int64         `json:"success_requests"`
	FailedRequests  int64         `json:"failed_requests"`
	CacheHits       int64         `json:"cache_hits"`
	CacheMisses     int64         `json:"cache_misses"`
	AvgResponseTime time.Duration `json:"avg_response_time"`
	MaxResponseTime time.Duration `json:"max_response_time"`
}

// RetryPolicy 定义重试策略
type RetryPolicy struct {
	MaxAttempts  int           `json:"max_attempts"`  // 最大重试次数
	InitialDelay time.Duration `json:"initial_delay"` // 初始延迟
	MaxDelay     time.Duration `json:"max_delay"`     // 最大延迟
	Multiplier   float64       `json:"multiplier"`    // 延迟倍数
}

// DefaultRetryPolicy 返回默认的重试策略
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:  3,
		InitialDelay: 500 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		Multiplier:   2.0,
	}
}
