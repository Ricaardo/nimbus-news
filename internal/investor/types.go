package investor

// InvestmentStyle 投资风格枚举
type InvestmentStyle string

const (
	StyleVALUE        InvestmentStyle = "VALUE"        // 价值投资
	StyleTREND        InvestmentStyle = "TREND"        // 趋势投资
	StyleCONTRARIAN   InvestmentStyle = "CONTRARIAN"   // 逆向投资
	StyleQUANTITATIVE InvestmentStyle = "QUANTITATIVE" // 量化投资
	StyleALL_WEATHER  InvestmentStyle = "ALL_WEATHER"  // 全天候策略
	StyleMOMENTUM    InvestmentStyle = "MOMENTUM"      // 动量投资
)

// StyleName 获取风格名称
func (s InvestmentStyle) Name() string {
	switch s {
	case StyleVALUE:
		return "价值投资"
	case StyleTREND:
		return "趋势投资"
	case StyleCONTRARIAN:
		return "逆向投资"
	case StyleQUANTITATIVE:
		return "量化投资"
	case StyleALL_WEATHER:
		return "全天候策略"
	case StyleMOMENTUM:
		return "动量投资"
	default:
		return string(s)
	}
}
