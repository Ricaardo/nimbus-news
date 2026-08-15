// Package model — market types now live in the shared datasources module
// (moved 2026-07 R6-a); these aliases keep news source-compatible.
package model

import types "github.com/Ricaardo/nimbus-os/datasources/market/types"

type (
	MarketQuote      = types.MarketQuote
	IndexQuote       = types.IndexQuote
	KLineItem        = types.KLineItem
	SecurityAnalysis = types.SecurityAnalysis
	SentimentData    = types.SentimentData
	NewsItem         = types.NewsItem
	IPOInfo          = types.IPOInfo
)
