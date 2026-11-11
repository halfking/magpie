package core

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestBalanceManager_Basic(t *testing.T) {
	config := DefaultManagerConfig()
	manager := NewBalanceManager(config)

	if manager == nil {
		t.Fatal("NewBalanceManager returned nil")
	}

	if manager.registry == nil {
		t.Error("registry should not be nil")
	}

	if manager.cache == nil {
		t.Error("cache should not be nil")
	}
}

func TestBalanceManager_RegisterProvider(t *testing.T) {
	manager := NewBalanceManager(DefaultManagerConfig())

	// 创建一个 mock provider
	mockProvider := &mockBalanceProvider{
		name:  "test-provider",
		hosts: []string{"test.example.com"},
	}

	// 注册
	err := manager.RegisterProvider(mockProvider)
	if err != nil {
		t.Errorf("RegisterProvider failed: %v", err)
	}

	// 验证注册成功
	provider := manager.registry.Get("test-provider")
	if provider == nil {
		t.Error("provider not found after registration")
	}

	// 重复注册应该失败
	err = manager.RegisterProvider(mockProvider)
	if err == nil {
		t.Error("duplicate registration should fail")
	}
}

func TestBalanceManager_GetBalance_Cache(t *testing.T) {
	manager := NewBalanceManager(ManagerConfig{
		CacheTTL:       1 * time.Second,
		CacheMaxSize:   10,
		MaxConcurrent:  5,
		RequestTimeout: 5 * time.Second,
		RetryPolicy:    DefaultRetryPolicy(),
	})

	mockProvider := &mockBalanceProvider{
		name:  "test-cache",
		hosts: []string{"cache.example.com"},
		result: &BalanceResult{
			Amount:   "$10.00",
			Currency: "USD",
		},
	}

	_ = manager.RegisterProvider(mockProvider)

	config := ProviderConfig{
		ProviderID:   "test-cache",
		APIKey:       "test-key",
		ChatEndpoint: "https://cache.example.com/v1/chat",
		Timeout:      5 * time.Second,
	}

	ctx := context.Background()

	// 第一次查询 - 应该命中 provider
	result1, err := manager.GetBalance(ctx, config)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	if result1.Amount != "$10.00" {
		t.Errorf("unexpected amount: %s", result1.Amount)
	}

	// 第二次查询 - 应该命中缓存
	result2, err := manager.GetBalance(ctx, config)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	if result2.Amount != "$10.00" {
		t.Errorf("unexpected amount: %s", result2.Amount)
	}

	// 验证缓存命中
	metrics := manager.GetMetrics()
	if metrics.CacheHits == 0 {
		t.Error("expected cache hit but got none")
	}
}

func TestBalanceManager_GetBalanceBatch(t *testing.T) {
	manager := NewBalanceManager(DefaultManagerConfig())

	// 注册多个 mock providers
	for i := 1; i <= 3; i++ {
		mockProvider := &mockBalanceProvider{
			name:  fmt.Sprintf("test-%d", i),
			hosts: []string{fmt.Sprintf("test%d.example.com", i)},
			result: &BalanceResult{
				Amount:   fmt.Sprintf("$%d.00", i*10),
				Currency: "USD",
			},
		}
		_ = manager.RegisterProvider(mockProvider)
	}

	// 准备配置
	configs := []ProviderConfig{
		{ProviderID: "test-1", APIKey: "key1", ChatEndpoint: "https://test1.example.com/v1/chat", Timeout: 5 * time.Second},
		{ProviderID: "test-2", APIKey: "key2", ChatEndpoint: "https://test2.example.com/v1/chat", Timeout: 5 * time.Second},
		{ProviderID: "test-3", APIKey: "key3", ChatEndpoint: "https://test3.example.com/v1/chat", Timeout: 5 * time.Second},
	}

	ctx := context.Background()

	// 批量查询
	results, err := manager.GetBalanceBatch(ctx, configs)
	if err != nil {
		t.Fatalf("GetBalanceBatch failed: %v", err)
	}

	// 验证结果
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
}

func TestBalanceManager_Metrics(t *testing.T) {
	manager := NewBalanceManager(DefaultManagerConfig())

	// 重置指标
	manager.ResetMetrics()

	metrics := manager.GetMetrics()
	if metrics.TotalRequests != 0 {
		t.Error("metrics should be reset to zero")
	}
}

func TestMemoryCache_Basic(t *testing.T) {
	cache := NewMemoryCache(10)

	result := &BalanceResult{
		Amount:   "$10.00",
		Currency: "USD",
	}

	// 测试 Set 和 Get
	cache.Set("test-key", result, 1*time.Second)

	retrieved, ok := cache.Get("test-key")
	if !ok {
		t.Error("expected cache hit")
	}

	if retrieved.Amount != "$10.00" {
		t.Errorf("unexpected amount: %s", retrieved.Amount)
	}

	// 测试过期
	time.Sleep(1500 * time.Millisecond)
	_, ok = cache.Get("test-key")
	if ok {
		t.Error("expected cache miss after expiration")
	}
}

func TestMemoryCache_Eviction(t *testing.T) {
	cache := NewMemoryCache(3) // 最多 3 个条目

	// 添加 4 个条目
	for i := 1; i <= 4; i++ {
		result := &BalanceResult{Amount: fmt.Sprintf("$%d.00", i)}
		cache.Set(fmt.Sprintf("key-%d", i), result, 1*time.Minute)
	}

	stats := cache.Stats()
	if stats.Size > 3 {
		t.Errorf("cache size %d exceeds max size 3", stats.Size)
	}

	if stats.Evictions == 0 {
		t.Error("expected at least one eviction")
	}
}

func TestProviderRegistry_Basic(t *testing.T) {
	registry := NewProviderRegistry()

	mockProvider := &mockBalanceProvider{
		name:  "test-registry",
		hosts: []string{"registry.example.com"},
	}

	// 注册
	err := registry.Register(mockProvider)
	if err != nil {
		t.Errorf("Register failed: %v", err)
	}

	// 通过名称获取
	provider := registry.Get("test-registry")
	if provider == nil {
		t.Error("provider not found")
	}

	// 通过 host 获取
	provider = registry.GetByHost("registry.example.com")
	if provider == nil {
		t.Error("provider not found by host")
	}

	// 列出所有
	names := registry.ListNames()
	if len(names) != 1 {
		t.Errorf("expected 1 provider, got %d", len(names))
	}
}

// Mock BalanceProvider for testing
type mockBalanceProvider struct {
	name       string
	hosts      []string
	result     *BalanceResult
	err        error
	deprecated bool
	message    string
}

func (m *mockBalanceProvider) Name() string {
	return m.name
}

func (m *mockBalanceProvider) Hosts() []string {
	return m.hosts
}

func (m *mockBalanceProvider) FetchBalance(ctx context.Context, config ProviderConfig) (*BalanceResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.result, nil
}

func (m *mockBalanceProvider) ParseResponse(body []byte) (*BalanceResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.result, nil
}

func (m *mockBalanceProvider) IsDeprecated() bool {
	return m.deprecated
}

func (m *mockBalanceProvider) DeprecationMessage() string {
	return m.message
}
