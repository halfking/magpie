package core

import (
	"fmt"
	"sync"
)

// ProviderRegistry Provider 注册中心
// 管理所有已注册的 balance provider
type ProviderRegistry struct {
	providers    map[string]BalanceProvider // key: provider name
	hostMap      map[string]BalanceProvider // key: host -> provider
	genericProvider BalanceProvider         // 通用 provider（用于自定义 URL）
	mu           sync.RWMutex
}

// NewProviderRegistry 创建新的注册中心
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[string]BalanceProvider),
		hostMap:   make(map[string]BalanceProvider),
	}
}

// Register 注册一个 provider
func (r *ProviderRegistry) Register(provider BalanceProvider) error {
	if provider == nil {
		return fmt.Errorf("provider cannot be nil")
	}
	
	name := provider.Name()
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}
	
	r.mu.Lock()
	defer r.mu.Unlock()
	
	// 检查是否已注册
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("provider %s already registered", name)
	}
	
	// 注册 provider
	r.providers[name] = provider
	
	// 注册 host 映射
	for _, host := range provider.Hosts() {
		if host != "" {
			r.hostMap[host] = provider
		}
	}
	
	return nil
}

// Unregister 注销一个 provider
func (r *ProviderRegistry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	provider, ok := r.providers[name]
	if !ok {
		return fmt.Errorf("provider %s not found", name)
	}
	
	// 移除 host 映射
	for _, host := range provider.Hosts() {
		delete(r.hostMap, host)
	}
	
	// 移除 provider
	delete(r.providers, name)
	
	return nil
}

// Get 根据名称获取 provider
func (r *ProviderRegistry) Get(name string) BalanceProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	return r.providers[name]
}

// GetByHost 根据 host 获取对应的 provider
func (r *ProviderRegistry) GetByHost(host string) BalanceProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	return r.hostMap[host]
}

// SetGenericProvider 设置通用 provider（用于处理自定义 URL）
func (r *ProviderRegistry) SetGenericProvider(provider BalanceProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	r.genericProvider = provider
}

// GetGenericProvider 获取通用 provider
func (r *ProviderRegistry) GetGenericProvider() BalanceProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	return r.genericProvider
}

// List 列出所有已注册的 provider
func (r *ProviderRegistry) List() []BalanceProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	list := make([]BalanceProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		list = append(list, provider)
	}
	
	return list
}

// ListNames 列出所有已注册的 provider 名称
func (r *ProviderRegistry) ListNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	
	return names
}

// Count 返回已注册的 provider 数量
func (r *ProviderRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	return len(r.providers)
}

// Clear 清空所有注册的 provider
func (r *ProviderRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	r.providers = make(map[string]BalanceProvider)
	r.hostMap = make(map[string]BalanceProvider)
	r.genericProvider = nil
}
