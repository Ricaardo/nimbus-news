package portfolio

import "errors"

var (
	// ErrPositionNotFound 持仓不存在
	ErrPositionNotFound = errors.New("position not found")

	// ErrInsufficientQuantity 持仓数量不足
	ErrInsufficientQuantity = errors.New("insufficient quantity")

	// ErrInvalidQuantity 无效数量
	ErrInvalidQuantity = errors.New("invalid quantity")

	// ErrInvalidPrice 无效价格
	ErrInvalidPrice = errors.New("invalid price")

	// ErrSymbolRequired 标的代码必填
	ErrSymbolRequired = errors.New("symbol is required")
)
