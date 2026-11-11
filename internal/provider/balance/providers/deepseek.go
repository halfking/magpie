package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/provider/balance/core"
)

// DeepSeekProvider DeepSeek 余额提供者
type DeepSeekProvider struct{}

// NewDeepSeekProvider 创建 DeepSeek provider
func NewDeepSeekProvider() *DeepSeekProvider {
	return &DeepSeekProvider{}
}

// Name 返回 provider 名称
func (p *DeepSeekProvider) Name() string {
	return "deepseek"
}

// Hosts 返回支持的 host 列表
func (p *DeepSeekProvider) Hosts() []string {
	return []string{"api.deepseek.com"}
}

// FetchBalance 获取余额
func (p *DeepSeekProvider) FetchBalance(ctx context.Context, config core.ProviderConfig) (*core.BalanceResult, error) {
	// 构建请求
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.deepseek.com/user/balance", nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	// 设置认证头
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Accept", "application/json")

	// 发送请求
	client := &http.Client{Timeout: config.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 限制 1MB
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	// 检查状态码
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	result, err := p.ParseResponse(body)
	if err != nil {
		return nil, err
	}

	result.RawResponse = body
	return result, nil
}

// ParseResponse 解析响应
func (p *DeepSeekProvider) ParseResponse(body []byte) (*core.BalanceResult, error) {
	var resp struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency        string      `json:"currency"`
			TotalBalance    interface{} `json:"total_balance"`
			GrantedBalance  interface{} `json:"granted_balance"`
			ToppedUpBalance interface{} `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse JSON failed: %w", err)
	}

	if len(resp.BalanceInfos) == 0 {
		return nil, errors.New("no balance info in response")
	}

	// 解析各币种余额
	type balanceAmount struct {
		currency string
		total    float64
		granted  float64
		topped   float64
		hasSplit bool
	}

	var amounts []balanceAmount
	for _, info := range resp.BalanceInfos {
		total, ok := parseNumber(info.TotalBalance)
		if !ok {
			continue
		}

		amt := balanceAmount{
			currency: strings.ToUpper(info.Currency),
			total:    total,
		}

		// 尝试解析拆分信息
		granted, gok := parseNumber(info.GrantedBalance)
		topped, tok := parseNumber(info.ToppedUpBalance)
		if gok && tok {
			amt.granted = granted
			amt.topped = topped
			amt.hasSplit = true
		}

		amounts = append(amounts, amt)
	}

	if len(amounts) == 0 {
		return nil, errors.New("no valid balance found")
	}

	// 按优先级排序：有余额的在前，按币种代码排序
	// 这里简化处理，实际应该更复杂

	// 构建显示金额
	parts := make([]string, 0, len(amounts))
	var allParts []core.BalancePart
	var split []core.BalancePart

	for _, amt := range amounts {
		sign := currencySign(amt.currency)
		parts = append(parts, formatMoney(sign, amt.total))

		// 如果有拆分信息且赠送部分>0，添加到 split
		if amt.hasSplit && amt.granted > 0 {
			split = append(split,
				core.BalancePart{
					Label: "Granted",
					Text:  formatMoney(sign, amt.granted),
				},
				core.BalancePart{
					Label: "Topped up",
					Text:  formatMoney(sign, amt.topped),
				},
			)
		}
	}

	return &core.BalanceResult{
		Amount:   strings.Join(parts, " · "),
		Parts:    allParts,
		Split:    split,
		Currency: amounts[0].currency,
		IsShared: true, // DeepSeek 余额是账户级别的
	}, nil
}

// IsDeprecated DeepSeek API 未废弃
func (p *DeepSeekProvider) IsDeprecated() bool {
	return false
}

// DeprecationMessage 返回废弃说明
func (p *DeepSeekProvider) DeprecationMessage() string {
	return ""
}

// parseNumber 解析数字（支持字符串和数字类型）
func parseNumber(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// currencySign 返回币种符号
func currencySign(code string) string {
	switch strings.ToUpper(code) {
	case "CNY", "RMB":
		return "¥"
	case "USD":
		return "$"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	case "":
		return ""
	default:
		return strings.ToUpper(code) + " "
	}
}

// formatMoney 格式化金额
func formatMoney(sign string, value float64) string {
	return sign + strconv.FormatFloat(value, 'f', 2, 64)
}
