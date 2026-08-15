package investors

// RegisterAll 注册所有内置投资人
func RegisterAll() {
	// 各投资人已在 init() 中自动注册
	// 此函数用于显式调用确保注册
	_ = Buffett
	_ = Soros
	_ = Munger
	_ = Dalio
	_ = Lynch
}
