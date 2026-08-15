package source

import (
	"time"
)

// IsTradingDayCN 判断日期是否为 A 股交易日 (简化版: 仅判周末)
// 节假日识别精度不足 — 若需要完整识别, 可在 Go 层接 akshare 日历缓存 (TODO)
// 但法定长假期间运维会手动禁用对应源, 此处周末判断已足够 80% 场景
func IsTradingDayCN(t time.Time) bool {
	w := t.Weekday()
	return w != time.Saturday && w != time.Sunday
}

// IsTradingTimeCN 判断当前是否 A 股交易时段 (不含集合竞价)
// 北京时间 09:30-11:30 + 13:00-15:00
func IsTradingTimeCN(t time.Time) bool {
	if !IsTradingDayCN(t) {
		return false
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err == nil {
		t = t.In(loc)
	}
	h, m := t.Hour(), t.Minute()
	mins := h*60 + m
	// 09:30-11:30
	if mins >= 9*60+30 && mins <= 11*60+30 {
		return true
	}
	// 13:00-15:00
	if mins >= 13*60 && mins <= 15*60 {
		return true
	}
	return false
}

// IsAShareBroadlyActive 宽松判断: 盘前/盘中/盘后当日均为 true, 仅周末 false
// 用于 pre-market-briefing/closing-briefing 这类 schedule 型源
func IsAShareBroadlyActive(t time.Time) bool {
	return IsTradingDayCN(t)
}
