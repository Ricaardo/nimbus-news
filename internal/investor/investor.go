package investor

import (
	"fmt"
	"strings"
	"sync"
)

// InvestorSkill 投资人技能
type InvestorSkill struct {
	ID                     string          // 唯一标识: "buffett"
	Name                   string          // 名称: "沃伦·巴菲特"
	Title                  string          // 称号: "价值投资大师"
	Style                  InvestmentStyle // 投资风格

	// Prompt模板
	SystemPrompt          string // 系统人设Prompt
	DecisionPromptTemplate string // 决策Prompt模板（支持变量替换）

	// 投资参数
	MaxPosition        float64 // 最大仓位 80%
	MinPosition        float64 // 最小仓位
	StopLossPct        float64 // 止损线
	TakeProfitPct      float64 // 止盈线
	PreferHoldingDays  int     // 偏好持有天数
}

// BuildDecisionPrompt 构建决策Prompt，支持变量替换
func (i *InvestorSkill) BuildDecisionPrompt(context map[string]interface{}) string {
	prompt := i.DecisionPromptTemplate

	// 替换变量
	for key, value := range context {
		placeholder := fmt.Sprintf("{%s}", key)
		var replacement string

		switch v := value.(type) {
		case string:
			replacement = v
		case float64:
			replacement = fmt.Sprintf("%.2f", v)
		case int:
			replacement = fmt.Sprintf("%d", v)
		case bool:
			if v {
				replacement = "是"
			} else {
				replacement = "否"
			}
		default:
			replacement = fmt.Sprintf("%v", v)
		}

		prompt = strings.ReplaceAll(prompt, placeholder, replacement)
	}

	return prompt
}

// InvestorRegistry 投资人注册中心
type InvestorRegistry struct {
	investors map[string]*InvestorSkill
	mu        sync.RWMutex
}

// 全局默认注册中心
var defaultRegistry = &InvestorRegistry{
	investors: make(map[string]*InvestorSkill),
}

// Register 注册投资人
func (r *InvestorRegistry) Register(skill *InvestorSkill) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.investors[skill.ID] = skill
}

// Get 获取投资人
func (r *InvestorRegistry) Get(investorID string) (*InvestorSkill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.investors[investorID]
	return skill, ok
}

// ListAll 列出所有投资人
func (r *InvestorRegistry) ListAll() []*InvestorSkill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*InvestorSkill, 0, len(r.investors))
	for _, skill := range r.investors {
		result = append(result, skill)
	}
	return result
}

// Register 注册投资人到全局注册中心
func Register(skill *InvestorSkill) {
	defaultRegistry.Register(skill)
}

// Get 获取投资人从全局注册中心
func Get(investorID string) (*InvestorSkill, bool) {
	return defaultRegistry.Get(investorID)
}

// ListAll 列出所有投资人从全局注册中心
func ListAll() []*InvestorSkill {
	return defaultRegistry.ListAll()
}
