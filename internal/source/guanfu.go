package source

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Ricaardo/nimbus-os/guanfu/pkg/client"
	"github.com/Ricaardo/nimbus-os/guanfu/pkg/engine"
	"github.com/Ricaardo/nimbus-os/guanfu/pkg/history"
	"github.com/Ricaardo/nimbus-os/guanfu/pkg/model"

	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	msgmodel "github.com/Ricaardo/nimbus-os/news/internal/model"
)

// GuanfuLLM 全局 LLM Provider，用于观复盘面解读
var GuanfuLLM llm.Provider

// SetGuanfuLLM 设置观复解读 LLM
func SetGuanfuLLM(provider llm.Provider) {
	GuanfuLLM = provider
}

func init() {
	Register("guanfu", NewGuanfuSource)
}

// GuanfuSource 观复 BTC 盘面 + AI 解读
type GuanfuSource struct {
	name string
}

func NewGuanfuSource(cfg Config) (Source, error) {
	return &GuanfuSource{name: cfg.Name}, nil
}

func (s *GuanfuSource) Name() string { return s.name }
func (s *GuanfuSource) Type() string { return "guanfu" }

func (s *GuanfuSource) Fetch() ([]*msgmodel.Message, error) {
	slog.Debug("fetching guanfu data", "source", s.name)

	cfg := &model.Config{
		Thresholds: model.Thresholds{
			BTCMAFast: 120, BTCMASlow: 200, TopCoinCount: 50,
			AHRHalfLifeDays: 365 * 4,
		},
		API: model.APIConfig{Timeout: "10s", Retries: 3, Mock: false},
	}

	ctx := context.Background()
	snapshot, err := client.NewRealClient().GetSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("guanfu snapshot: %w", err)
	}

	calculator := engine.NewCalculator(cfg)
	if hist, err := history.Open("data/guanfu_history.db"); err == nil {
		calculator.WithHistory(hist)
		defer hist.Close()
	}

	panel := calculator.BuildPanel(snapshot)
	verdict := engine.BuildVerdict(panel)

	// AI 解读是消息主体
	aiComment := ""
	if GuanfuLLM != nil {
		aiComment = s.interpret(ctx, snapshot, panel, verdict)
	}

	msg := s.buildMessage(snapshot, panel, verdict, aiComment)
	return []*msgmodel.Message{msg}, nil
}

// interpret 生成面向普通用户的解读，不超过 300 字
func (s *GuanfuSource) interpret(ctx context.Context, snap *model.MarketSnapshot, panel *model.IndicatorPanel, v *engine.Verdict) string {
	btc := snap.BTCPrice.InexactFloat64()
	fg := snap.FearGreedIndex.InexactFloat64()

	// 给 LLM 的上下文：用中文结构化字段，不堆原始指标
	var ctx_lines []string
	ctx_lines = append(ctx_lines, fmt.Sprintf("BTC 当前价格: $%.0f", btc))
	ctx_lines = append(ctx_lines, fmt.Sprintf("市场情绪: 恐贪指数 %.0f", fg))
	ctx_lines = append(ctx_lines, fmt.Sprintf("观复读盘: %s（信号一致性 %+d/8，置信度 %s）", v.Stance, v.NetDirection, v.Confidence))
	ctx_lines = append(ctx_lines, fmt.Sprintf("顶部接近度 %.0f%% / 底部接近度 %.0f%%", v.TopProximity*100, v.BottomProximity*100))

	if len(v.Reasons) > 0 {
		ctx_lines = append(ctx_lines, "支撑当前判断的信号: "+strings.Join(v.Reasons, "；"))
	}
	if len(v.CounterEvidence) > 0 {
		ctx_lines = append(ctx_lines, "反向信号（需警惕）: "+strings.Join(v.CounterEvidence, "；"))
	}
	if len(v.KillCriteria) > 0 {
		ctx_lines = append(ctx_lines, "判断失效条件: "+v.KillCriteria[0])
	}

	// 补充 1-2 个关键数值（选最有感知度的）
	if ind, ok := panel.Valuation["mvrv_z_score"]; ok && ind.IsAvailable() {
		ctx_lines = append(ctx_lines, fmt.Sprintf("MVRV-Z: %.2f（>3 历史高估区，<0 历史低估区）", ind.Value))
	}
	if ind, ok := panel.Positioning["funding_rate_pct"]; ok && ind.IsAvailable() {
		ctx_lines = append(ctx_lines, fmt.Sprintf("合约资金费率: %.4f%%（正值=多头付费，过高=过热）", ind.Value))
	}

	prompt := fmt.Sprintf(`你是一位加密货币投资顾问，正在给普通投资者写每日市场简报。

以下是今日观复系统的盘面数据：
%s

请用通俗易懂的中文写一段 200 字以内的解读，要求：
1. 第一句话直接说结论（现在是什么市场状态，适合做什么）
2. 用一两句话解释为什么（不要堆数字，说清楚逻辑）
3. 如果有风险或需要注意的地方，简短提示
4. 语气自然，像朋友聊天，不要用"综上所述"等套话
5. 不要重复数字，不要列指标名称

直接输出正文，不要标题。`, strings.Join(ctx_lines, "\n"))

	content, _, err := GuanfuLLM.Think(ctx, []llm.Message{{Role: "user", Content: prompt}}, "thinking")
	if err != nil {
		slog.Warn("guanfu: AI interpret failed", "error", err)
		return ""
	}
	if len([]rune(content)) > 300 {
		runes := []rune(content)
		content = string(runes[:300])
	}
	return content
}

func (s *GuanfuSource) buildMessage(
	snap *model.MarketSnapshot,
	panel *model.IndicatorPanel,
	v *engine.Verdict,
	aiComment string,
) *msgmodel.Message {
	btc := snap.BTCPrice.InexactFloat64()
	fg := snap.FearGreedIndex.InexactFloat64()

	// Title：读盘口径 + 价格，简洁
	title := fmt.Sprintf("🔭 观复读盘 · %s | BTC $%.0f", v.Stance, btc)

	// Content：AI 解读为主，数据摘要为辅
	var b strings.Builder

	if aiComment != "" {
		b.WriteString(aiComment)
		b.WriteString("\n\n")
	} else {
		// AI 不可用时，用 Verdict 结构化字段拼降级解读
		b.WriteString(fallbackInterpret(v, btc, fg))
		b.WriteString("\n\n")
	}

	// 数据摘要行（一行，给想看数字的人）
	b.WriteString("── 数据摘要 ──\n")
	summaryParts := []string{
		fmt.Sprintf("BTC $%.0f", btc),
		fmt.Sprintf("恐贪 %.0f", fg),
		fmt.Sprintf("信号 %+d/8", v.NetDirection),
		fmt.Sprintf("置信 %s", v.Confidence),
	}
	if ind, ok := panel.Valuation["mvrv_z_score"]; ok && ind.IsAvailable() {
		summaryParts = append(summaryParts, fmt.Sprintf("MVRV-Z %.2f", ind.Value))
	}
	if ind, ok := panel.Valuation["ahr999_compressed"]; ok && ind.IsAvailable() {
		summaryParts = append(summaryParts, fmt.Sprintf("AHR999 %.2f", ind.Value))
	}
	b.WriteString(strings.Join(summaryParts, " | "))
	b.WriteString("\n")

	// 失效条件（简短，给有经验的人）
	if len(v.KillCriteria) > 0 {
		b.WriteString(fmt.Sprintf("⚠️ 失效: %s\n", v.KillCriteria[0]))
	}

	// ShortContent：只有解读文字，给微信
	shortContent := aiComment
	if shortContent == "" {
		shortContent = fallbackInterpret(v, btc, fg)
	}

	msg := &msgmodel.Message{
		Type:         msgmodel.TypeAnalysis,
		ID:           fmt.Sprintf("guanfu_%s_%d", v.Date, v.NetDirection),
		Title:        title,
		Content:      b.String(),
		ShortContent: shortContent,
		Source:       s.name,
		SourceType:   "guanfu",
		CreateTime:   snap.Date,
		FetchTime:    time.Now(),
		Tags:         []string{"crypto", "btc", "guanfu", "观复"},
		Metadata: map[string]interface{}{
			"net_direction":    v.NetDirection,
			"coverage":         v.Coverage,
			"confidence":       v.Confidence,
			"regime":           v.Regime,
			"stance":           v.Stance,
			"top_proximity":    v.TopProximity,
			"bottom_proximity": v.BottomProximity,
			"btc_price":        btc,
			"fear_greed":       fg,
			"ai_interpreted":   aiComment != "",
		},
	}

	return msg
}

// fallbackInterpret 当 AI 不可用时，用 Verdict 结构化字段生成可读解读。
func fallbackInterpret(v *engine.Verdict, btc, fg float64) string {
	var b strings.Builder

	// 状态一句话
	b.WriteString(fmt.Sprintf("当前读盘：%s。", v.Stance))

	// 情绪描述
	switch {
	case fg >= 75:
		b.WriteString("市场情绪极度贪婪，需警惕过热风险。")
	case fg >= 55:
		b.WriteString("市场情绪偏乐观。")
	case fg <= 25:
		b.WriteString("市场情绪极度恐慌，历史上往往是布局机会。")
	case fg <= 45:
		b.WriteString("市场情绪偏悲观。")
	default:
		b.WriteString("市场情绪中性。")
	}

	// 支撑信号
	if len(v.Reasons) > 0 {
		b.WriteString("\n主要支撑：" + strings.Join(v.Reasons[:min(2, len(v.Reasons))], "；") + "。")
	}

	// 反证
	if len(v.CounterEvidence) > 0 {
		b.WriteString("\n需注意：" + v.CounterEvidence[0] + "。")
	}

	// 失效条件
	if len(v.KillCriteria) > 0 {
		b.WriteString("\n⚠️ 若" + v.KillCriteria[0] + "，判断失效。")
	}

	return b.String()
}
