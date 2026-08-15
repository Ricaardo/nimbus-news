package core

import "errors"

// Agent 相关错误
var (
	ErrSensitiveContent     = errors.New("sensitive content detected")
	ErrNotInvestmentRelated = errors.New("not investment related")
	ErrMarketUnavailable    = errors.New("market service unavailable")
	ErrNewsUnavailable      = errors.New("news store unavailable")
	ErrLLMUnavailable       = errors.New("LLM service unavailable")
	ErrSymbolNotFound       = errors.New("symbol not found")
	ErrEmptyResponse        = errors.New("empty response")
)
