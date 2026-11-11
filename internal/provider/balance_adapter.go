package provider

import (
	"context"
	"time"
	
	"github.com/yetone/magpie/internal/provider/balance/core"
	"github.com/yetone/magpie/internal/provider/balance/providers"
)

// 全局的 BalanceManager 实例
var defaultBalanceManager *core.BalanceManager

func init() {
	// 初始化默认的 BalanceManager
	config := core.DefaultManagerConfig()
	defaultBalanceManager = core.NewBalanceManager(config)
	
	// 注册内置 providers
	registerBuiltinProviders()
}

// registerBuiltinProviders 注册所有内置的 providers
func registerBuiltinProviders() {
	// 注册 DeepSeek
	_ = defaultBalanceManager.RegisterProvider(providers.NewDeepSeekProvider())
	
	// TODO: 注册其他 providers
	// _ = defaultBalanceManager.RegisterProvider(providers.NewMoonshotProvider())
	// _ = defaultBalanceManager.RegisterProvider(providers.NewOpenRouterProvider())
	// ...
}

// BalanceV2 新的余额查询接口（供内部使用）
// 返回完整的 BalanceResult，包含更多元数据
func BalanceV2(ctx context.Context, p Provider) (*core.BalanceResult, error) {
	config := core.ProviderConfig{
		ProviderID:        p.ID,
		APIKey:            p.Key,
		BalanceToken:      p.BalanceToken,
		BalanceURL:        p.BalanceURL,
		BalancePath:       p.BalancePath,
		BalanceMethod:     p.BalanceMethod,
		Headers:           p.Headers,
		Timeout:           30 * time.Second,
		EnableRetry:       true,
		ChatEndpoint:      p.Chat,
		ResponsesEndpoint: p.Responses,
		AnthropicEndpoint: p.Anthropic,
	}
	
	return defaultBalanceManager.GetBalance(ctx, config)
}

// BalanceBatch 批量查询余额（新接口）
func BalanceBatch(ctx context.Context, providers []Provider) (map[string]*core.BalanceResult, error) {
	configs := make([]core.ProviderConfig, 0, len(providers))
	
	for _, p := range providers {
		config := core.ProviderConfig{
			ProviderID:        p.ID,
			APIKey:            p.Key,
			BalanceToken:      p.BalanceToken,
			BalanceURL:        p.BalanceURL,
			BalancePath:       p.BalancePath,
			BalanceMethod:     p.BalanceMethod,
			Headers:           p.Headers,
			Timeout:           30 * time.Second,
			EnableRetry:       true,
			ChatEndpoint:      p.Chat,
			ResponsesEndpoint: p.Responses,
			AnthropicEndpoint: p.Anthropic,
		}
		configs = append(configs, config)
	}
	
	return defaultBalanceManager.GetBalanceBatch(ctx, configs)
}

// GetBalanceManager 获取全局的 BalanceManager（供测试使用）
func GetBalanceManager() *core.BalanceManager {
	return defaultBalanceManager
}

// ClearBalanceCache 清空余额缓存
func ClearBalanceCache() {
	defaultBalanceManager.ClearCache()
}

// GetBalanceMetrics 获取余额查询的监控指标
func GetBalanceMetrics() core.BalanceMetrics {
	return defaultBalanceManager.GetMetrics()
}

// ResetBalanceMetrics 重置监控指标
func ResetBalanceMetrics() {
	defaultBalanceManager.ResetMetrics()
}
