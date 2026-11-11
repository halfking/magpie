package providers

import (
	"context"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider/balance/core"
)

func TestDeepSeekProvider_Basic(t *testing.T) {
	provider := NewDeepSeekProvider()

	if provider.Name() != "deepseek" {
		t.Errorf("unexpected name: %s", provider.Name())
	}

	hosts := provider.Hosts()
	if len(hosts) != 1 || hosts[0] != "api.deepseek.com" {
		t.Errorf("unexpected hosts: %v", hosts)
	}

	if provider.IsDeprecated() {
		t.Error("DeepSeek should not be deprecated")
	}
}

func TestDeepSeekProvider_ParseResponse(t *testing.T) {
	provider := NewDeepSeekProvider()

	tests := []struct {
		name     string
		response string
		wantErr  bool
		expected string
	}{
		{
			name: "single currency CNY",
			response: `{
				"is_available": true,
				"balance_infos": [
					{
						"currency": "CNY",
						"total_balance": "110.00",
						"granted_balance": "10.00",
						"topped_up_balance": "100.00"
					}
				]
			}`,
			wantErr:  false,
			expected: "¥110.00",
		},
		{
			name: "multiple currencies",
			response: `{
				"is_available": true,
				"balance_infos": [
					{
						"currency": "CNY",
						"total_balance": "110.00"
					},
					{
						"currency": "USD",
						"total_balance": "20.50"
					}
				]
			}`,
			wantErr:  false,
			expected: "¥110.00 · $20.50",
		},
		{
			name: "number as float",
			response: `{
				"is_available": true,
				"balance_infos": [
					{
						"currency": "USD",
						"total_balance": 15.75
					}
				]
			}`,
			wantErr:  false,
			expected: "$15.75",
		},
		{
			name: "empty balance_infos",
			response: `{
				"is_available": true,
				"balance_infos": []
			}`,
			wantErr: true,
		},
		{
			name:     "invalid JSON",
			response: `invalid json`,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := provider.ParseResponse([]byte(tt.response))

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Amount != tt.expected {
				t.Errorf("amount = %q, want %q", result.Amount, tt.expected)
			}

			if result.Currency == "" {
				t.Error("currency should not be empty")
			}

			if !result.IsShared {
				t.Error("DeepSeek balance should be shared")
			}
		})
	}
}

func TestDeepSeekProvider_ParseResponse_WithSplit(t *testing.T) {
	provider := NewDeepSeekProvider()

	response := `{
		"is_available": true,
		"balance_infos": [
			{
				"currency": "CNY",
				"total_balance": "110.00",
				"granted_balance": "10.00",
				"topped_up_balance": "100.00"
			}
		]
	}`

	result, err := provider.ParseResponse([]byte(response))
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}

	if len(result.Split) != 2 {
		t.Errorf("expected 2 split parts, got %d", len(result.Split))
		return
	}

	// 验证拆分信息
	if result.Split[0].Label != "Granted" {
		t.Errorf("first split label = %q, want 'Granted'", result.Split[0].Label)
	}

	if result.Split[0].Text != "¥10.00" {
		t.Errorf("granted amount = %q, want '¥10.00'", result.Split[0].Text)
	}

	if result.Split[1].Label != "Topped up" {
		t.Errorf("second split label = %q, want 'Topped up'", result.Split[1].Label)
	}

	if result.Split[1].Text != "¥100.00" {
		t.Errorf("topped up amount = %q, want '¥100.00'", result.Split[1].Text)
	}
}

func TestDeepSeekProvider_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// 这是一个集成测试示例，需要真实的 API key
	// 在 CI 环境中应该跳过或使用 mock server
	t.Skip("integration test requires real API key")

	provider := NewDeepSeekProvider()

	config := core.ProviderConfig{
		ProviderID: "deepseek",
		APIKey:     "[REDACTED:secret]", // 替换为真实的 key
		Timeout:    10 * time.Second,
	}

	ctx := context.Background()
	result, err := provider.FetchBalance(ctx, config)

	if err != nil {
		t.Fatalf("FetchBalance failed: %v", err)
	}

	if result.Amount == "" {
		t.Error("amount should not be empty")
	}

	t.Logf("Balance: %s", result.Amount)
	if len(result.Split) > 0 {
		t.Logf("Split: %v", result.Split)
	}
}

func TestCurrencySign(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"CNY", "¥"},
		{"RMB", "¥"},
		{"USD", "$"},
		{"EUR", "€"},
		{"GBP", "£"},
		{"JPY", "JPY "},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got := currencySign(tt.code)
			if got != tt.want {
				t.Errorf("currencySign(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}

func TestParseNumber(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  float64
		ok    bool
	}{
		{"float64", 123.45, 123.45, true},
		{"string", "67.89", 67.89, true},
		{"string with spaces", "  10.5  ", 10.5, true},
		{"invalid string", "abc", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseNumber(tt.value)
			if ok != tt.ok {
				t.Errorf("parseNumber(%v) ok = %v, want %v", tt.value, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("parseNumber(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
