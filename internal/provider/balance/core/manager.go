package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// BalanceManager 余额管理器
// 负责协调各个 provider 的余额查询，提供缓存、并发、重试等功能
type BalanceManager struct {
	registry   *ProviderRegistry
	cache      BalanceCache
	httpClient *http.Client
	config     ManagerConfig
	metrics    *metrics
}

// ManagerConfig 管理器配置
type ManagerConfig struct {
	// 缓存配置
	CacheTTL     time.Duration // 缓存有效期（默认 5 分钟）
	CacheMaxSize int           // 缓存最大条目数（默认 1000）
	
	// 并发配置
	MaxConcurrent  int           // 最大并发数（默认 10）
	RequestTimeout time.Duration // 请求超时（默认 30 秒）
	
	// 重试配置
	RetryPolicy RetryPolicy
	
	// 探测配置
	EnableAutoProbe bool          // 是否启用自动探测
	ProbeTimeout    time.Duration // 探测超时
}

// DefaultManagerConfig 返回默认的管理器配置
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		CacheTTL:        5 * time.Minute,
		CacheMaxSize:    1000,
		MaxConcurrent:   10,
		RequestTimeout:  30 * time.Second,
		RetryPolicy:     DefaultRetryPolicy(),
		EnableAutoProbe: true,
		ProbeTimeout:    10 * time.Second,
	}
}

// metrics 内部监控指标
type metrics struct {
	totalRequests   atomic.Int64
	successRequests atomic.Int64
	failedRequests  atomic.Int64
	cacheHits       atomic.Int64
	cacheMisses     atomic.Int64
	responseTimes   []time.Duration
	maxResponseTime atomic.Int64 // 纳秒
	mu              sync.RWMutex  // 保护 responseTimes
}

// NewBalanceManager 创建新的余额管理器
func NewBalanceManager(config ManagerConfig) *BalanceManager {
	if config.CacheTTL == 0 {
		config = DefaultManagerConfig()
	}
	
	return &BalanceManager{
		registry: NewProviderRegistry(),
		cache:    NewMemoryCache(config.CacheMaxSize),
		httpClient: &http.Client{
			Timeout: config.RequestTimeout,
		},
		config:  config,
		metrics: &metrics{},
	}
}

// RegisterProvider 注册一个 provider
func (m *BalanceManager) RegisterProvider(provider BalanceProvider) error {
	return m.registry.Register(provider)
}

// GetBalance 获取单个 provider 的余额
func (m *BalanceManager) GetBalance(ctx context.Context, config ProviderConfig) (*BalanceResult, error) {
	m.metrics.totalRequests.Add(1)
	startTime := time.Now()
	
	// 生成缓存键
	cacheKey := m.generateCacheKey(config)
	
	// 尝试从缓存获取
	if cached, ok := m.cache.Get(cacheKey); ok {
		m.metrics.cacheHits.Add(1)
		return cached, nil
	}
	m.metrics.cacheMisses.Add(1)
	
	// 查找对应的 provider
	provider, err := m.findProvider(config)
	if err != nil {
		m.metrics.failedRequests.Add(1)
		return nil, err
	}
	
	// 执行余额查询（带重试）
	result, err := m.fetchWithRetry(ctx, provider, config)
	
	// 记录响应时间
	responseTime := time.Since(startTime)
	m.recordResponseTime(responseTime)
	
	if err != nil {
		m.metrics.failedRequests.Add(1)
		return nil, err
	}
	
	// 设置缓存
	result.CacheKey = cacheKey
	result.Timestamp = time.Now()
	m.cache.Set(cacheKey, result, m.config.CacheTTL)
	
	m.metrics.successRequests.Add(1)
	return result, nil
}

// GetBalanceBatch 批量获取多个 provider 的余额
// 使用 goroutine pool 并发查询，提升批量操作性能
func (m *BalanceManager) GetBalanceBatch(ctx context.Context, configs []ProviderConfig) (map[string]*BalanceResult, error) {
	results := make(map[string]*BalanceResult)
	errs := make(map[string]error)
	var mu sync.Mutex
	
	// 使用 semaphore 限制并发数
	sem := make(chan struct{}, m.config.MaxConcurrent)
	var wg sync.WaitGroup
	
	for _, cfg := range configs {
		wg.Add(1)
		go func(config ProviderConfig) {
			defer wg.Done()
			
			// 获取信号量
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }() // 释放信号量
			
			result, err := m.GetBalance(ctx, config)
			
			mu.Lock()
			defer mu.Unlock()
			
			key := config.ProviderID
			if key == "" {
				key = config.APIKey[:min(8, len(config.APIKey))]
			}
			
			if err != nil {
				errs[key] = err
			} else {
				results[key] = result
			}
		}(cfg)
	}
	
	wg.Wait()
	
	// 如果所有请求都失败，返回第一个错误
	if len(results) == 0 && len(errs) > 0 {
		for _, err := range errs {
			return nil, fmt.Errorf("all balance queries failed: %w", err)
		}
	}
	
	return results, nil
}

// GetMetrics 获取监控指标
func (m *BalanceManager) GetMetrics() BalanceMetrics {
	m.metrics.mu.RLock()
	defer m.metrics.mu.RUnlock()
	
	avgResponseTime := time.Duration(0)
	if len(m.metrics.responseTimes) > 0 {
		var total time.Duration
		for _, t := range m.metrics.responseTimes {
			total += t
		}
		avgResponseTime = total / time.Duration(len(m.metrics.responseTimes))
	}
	
	return BalanceMetrics{
		TotalRequests:   m.metrics.totalRequests.Load(),
		SuccessRequests: m.metrics.successRequests.Load(),
		FailedRequests:  m.metrics.failedRequests.Load(),
		CacheHits:       m.metrics.cacheHits.Load(),
		CacheMisses:     m.metrics.cacheMisses.Load(),
		AvgResponseTime: avgResponseTime,
		MaxResponseTime: time.Duration(m.metrics.maxResponseTime.Load()),
	}
}

// ResetMetrics 重置监控指标
func (m *BalanceManager) ResetMetrics() {
	m.metrics.totalRequests.Store(0)
	m.metrics.successRequests.Store(0)
	m.metrics.failedRequests.Store(0)
	m.metrics.cacheHits.Store(0)
	m.metrics.cacheMisses.Store(0)
	m.metrics.maxResponseTime.Store(0)
	
	m.metrics.mu.Lock()
	m.metrics.responseTimes = nil
	m.metrics.mu.Unlock()
}

// ClearCache 清空缓存
func (m *BalanceManager) ClearCache() {
	m.cache.Clear()
}

// findProvider 根据配置查找对应的 provider
func (m *BalanceManager) findProvider(config ProviderConfig) (BalanceProvider, error) {
	// 优先使用自定义 URL（通用 relay）
	if config.BalanceURL != "" {
		return m.registry.GetGenericProvider(), nil
	}
	
	// 根据 endpoint 的 host 查找
	hosts := []string{
		extractHost(config.ChatEndpoint),
		extractHost(config.ResponsesEndpoint),
		extractHost(config.AnthropicEndpoint),
	}
	
	for _, host := range hosts {
		if host != "" {
			if provider := m.registry.GetByHost(host); provider != nil {
				return provider, nil
			}
		}
	}
	
	return nil, fmt.Errorf("no balance provider found for config: %s", config.ProviderID)
}

// fetchWithRetry 执行带重试的余额查询
func (m *BalanceManager) fetchWithRetry(ctx context.Context, provider BalanceProvider, config ProviderConfig) (*BalanceResult, error) {
	policy := m.config.RetryPolicy
	var lastErr error
	delay := policy.InitialDelay
	
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if attempt > 0 {
			// 等待重试延迟
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			
			// 指数退避
			delay = time.Duration(float64(delay) * policy.Multiplier)
			if delay > policy.MaxDelay {
				delay = policy.MaxDelay
			}
		}
		
		result, err := provider.FetchBalance(ctx, config)
		if err == nil {
			return result, nil
		}
		
		// 检查是否为不可重试的错误
		if !isRetryableError(err) {
			return nil, err
		}
		
		lastErr = err
	}
	
	return nil, fmt.Errorf("after %d attempts: %w", policy.MaxAttempts, lastErr)
}

// generateCacheKey 生成缓存键
// 格式: provider_id:key_hash:query_type
func (m *BalanceManager) generateCacheKey(config ProviderConfig) string {
	keyHash := sha256.Sum256([]byte(config.APIKey))
	queryType := "default"
	if config.BalanceURL != "" {
		queryType = "custom"
	}
	return fmt.Sprintf("%s:%x:%s", config.ProviderID, keyHash[:8], queryType)
}

// recordResponseTime 记录响应时间
func (m *BalanceManager) recordResponseTime(t time.Duration) {
	m.metrics.mu.Lock()
	defer m.metrics.mu.Unlock()
	
	m.metrics.responseTimes = append(m.metrics.responseTimes, t)
	
	// 只保留最近 1000 次的响应时间
	if len(m.metrics.responseTimes) > 1000 {
		m.metrics.responseTimes = m.metrics.responseTimes[1:]
	}
	
	// 更新最大响应时间
	if ns := t.Nanoseconds(); ns > m.metrics.maxResponseTime.Load() {
		m.metrics.maxResponseTime.Store(ns)
	}
}

// extractHost 从 URL 中提取 host
func extractHost(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// 简单实现，实际应使用 url.Parse
	// 这里为了避免引入过多依赖，使用简化版本
	start := 0
	if idx := findSubstring(rawURL, "://"); idx >= 0 {
		start = idx + 3
	}
	end := len(rawURL)
	if idx := findChar(rawURL[start:], '/'); idx >= 0 {
		end = start + idx
	}
	return rawURL[start:end]
}

// isRetryableError 判断错误是否可重试
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// 简化实现：网络超时、临时错误等可重试
	// 实际应检查具体错误类型
	errStr := err.Error()
	return findSubstring(errStr, "timeout") >= 0 ||
		findSubstring(errStr, "temporary") >= 0 ||
		findSubstring(errStr, "connection refused") >= 0
}

// 辅助函数
func findSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func findChar(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
