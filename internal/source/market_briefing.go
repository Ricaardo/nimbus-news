package source

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"

	"github.com/Ricaardo/nimbus-os/datasources/market"
	"github.com/Ricaardo/nimbus-os/datasources/nasdaq"
)

func init() {
	Register("market-briefing", NewMarketBriefingSource)
}

// MarketBriefingSource 盘前/收盘/美盘前瞻 合并简报
// 将多个独立脚本的输出合并为一条消息，通过 LLM 生成叙事
type MarketBriefingSource struct {
	name            string
	briefingType    string // "pre_market", "closing", "us_preview", "aggregate"
	blockbeatsMode  string // "morning"/"evening",仅 aggregate 用
	pythonCmd       string
	scriptsDir      string
	marketService market.Service
	llmProvider   llm.Provider
	digestStore   digest.Store
	nasdaqClient  nasdaqCalendarClient
	nasdaqEnabled bool
}

type nasdaqCalendarClient interface {
	FetchCalendar(context.Context, string) ([]nasdaq.Event, error)
}

const digestBriefingItemLimit = 12

func (s *MarketBriefingSource) SetDigestStore(store digest.Store) {
	s.digestStore = store
}

// SetLLMProvider 实现 LLMSettable
func (s *MarketBriefingSource) SetLLMProvider(p llm.Provider) {
	s.llmProvider = p
}

func NewMarketBriefingSource(cfg Config) (Source, error) {
	s := &MarketBriefingSource{
		name:          cfg.Name,
		briefingType:  "closing", // default
		pythonCmd:     "python3",
		scriptsDir:    "scripts",
		marketService: market.NewMarketService(),
	}
	if v, ok := cfg.Options["briefing_type"].(string); ok && v != "" {
		s.briefingType = v
	}
	if v, ok := cfg.Options["blockbeats_mode"].(string); ok && v != "" {
		s.blockbeatsMode = v
	}
	s.nasdaqEnabled = s.briefingType == "us_preview"
	if v, ok := cfg.Options["nasdaq_calendar"].(bool); ok {
		s.nasdaqEnabled = v
	}
	if s.nasdaqEnabled && s.briefingType == "us_preview" {
		s.nasdaqClient = nasdaq.NewClient(nil, "")
	}
	if v, ok := cfg.Options["python_cmd"].(string); ok && v != "" {
		s.pythonCmd = v
	}
	if v, ok := cfg.Options["scripts_dir"].(string); ok && v != "" {
		s.scriptsDir = v
	}
	return s, nil
}

func (s *MarketBriefingSource) Name() string { return s.name }
func (s *MarketBriefingSource) Type() string { return "market-briefing" }

// Fetch 无父 ctx(兼容旧调用);FetchContext 供引擎/API 传入父 ctx 继承超时。
func (s *MarketBriefingSource) Fetch() ([]*model.Message, error) {
	return s.FetchContext(context.Background())
}

// FetchContext 根据 briefingType 分发到不同逻辑。
// parent 为上层调用方 ctx(API 主动拉取的 330s 兜底 / 调度链路源生命周期 ctx),
// 各 fetch 方法内部以 parent 派生自身超时,父 ctx 到期会级联取消。
func (s *MarketBriefingSource) FetchContext(parent context.Context) ([]*model.Message, error) {
	var (
		messages []*model.Message
		err      error
	)
	switch s.briefingType {
	case "pre_market":
		messages, err = s.fetchPreMarket(parent)
	case "closing":
		messages, err = s.fetchClosing(parent)
	case "us_preview":
		messages, err = s.fetchUSPreview(parent)
	case "aggregate":
		messages, err = s.fetchAggregate(parent)
	default:
		return nil, fmt.Errorf("unknown briefing_type: %s", s.briefingType)
	}
	// 兼容旧配置:us_preview 仍可租 us_preview 桶(现配置 briefing_target 已
	// 全部改为 news_aggregate,该桶为空,租约返回空不产生内容)。
	if err != nil || len(messages) == 0 || s.digestStore == nil || s.briefingType != "us_preview" {
		return messages, err
	}
	return s.appendDigest(parent, messages)
}

func (s *MarketBriefingSource) appendDigest(parent context.Context, messages []*model.Message) ([]*model.Message, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	briefing := digest.Briefing(s.briefingType)
	var (
		lease      *digest.Lease
		err        error
		topicAware bool
	)
	if topicStore, ok := s.digestStore.(digest.TopicStore); ok {
		lease, err = topicStore.LeaseTopics(ctx, briefing, digestBriefingItemLimit, 30*time.Minute)
		topicAware = true
	} else {
		lease, err = s.digestStore.Lease(ctx, briefing, digestBriefingItemLimit, 30*time.Minute)
	}
	if err != nil {
		return nil, fmt.Errorf("lease digest: %w", err)
	}
	if lease == nil {
		return messages, nil
	}
	var summary string
	if topicAware {
		summary = digest.RenderTopics(lease.Topics, digestBriefingItemLimit)
	} else {
		summary = digest.Render(lease.Items, digestBriefingItemLimit)
	}
	if summary == "" {
		return messages, nil
	}
	for _, msg := range messages {
		msg.Content = strings.TrimSpace(msg.Content) + "\n\n## 资讯摘要\n" + summary
		msg.SetMetadata("digest_lease_id", lease.ID)
		msg.SetMetadata("digest_briefing", s.briefingType)
	}
	return messages, nil
}

// us2cnSector maps US tickers to CN sector keywords for overseas trigger detection
var us2cnSector = map[string][]string{
	"SOXX":  {"半导体", "芯片"},
	"NVDA":  {"AI", "人工智能", "半导体"},
	"AMD":   {"AI", "半导体"},
	"TSLA":  {"新能源车", "锂电池"},
	"LCID":  {"新能源车"},
	"TAN":   {"光伏"},
	"XBI":   {"创新药", "生物医药", "医药"},
	"IBB":   {"创新药", "医药"},
	"KWEB":  {"互联网", "平台经济"},
	"BABA":  {"互联网", "电商"},
	"GDX":   {"贵金属", "黄金"},
	"GC=F":  {"贵金属", "黄金"},
	"XLE":   {"能源", "石油"},
	"XLF":   {"金融", "银行"},
	"XLY":   {"消费"},
	"XLI":   {"工业"},
	"MSFT":  {"云计算", "AI"},
	"GOOGL": {"云计算", "AI"},
}

type overseasSignal struct {
	Ticker    string
	ChangePct float64
	Sectors   []string
}

// ── pre_market: 盘前全景 (08:45) ──
// 海外隔夜行情 + 今日日历 + AI 叙事

type earningsCalendarItem struct {
	Code    string  `json:"code"`
	Name    string  `json:"name"`
	EPS     float64 `json:"eps"`
	Rev     float64 `json:"rev"`
	Date    string  `json:"date"`
}

type ipoCalendarItem struct {
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Price      float64 `json:"price"`
	MarketType string  `json:"market_type"`
	Date       string  `json:"date"`
}

type unlockCalendarItem struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Shares    float64 `json:"shares"`
	MarketCap float64 `json:"market_cap"`
	Date      string  `json:"date"`
}

// preMarketSession returns the trading session phase based on Asia/Shanghai time.
func preMarketSession(t time.Time) string {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	hour := t.In(shanghai).Hour()
	switch {
	case hour < 10:
		return "pre_market"
	case hour < 12:
		return "morning_close"
	case hour < 14:
		return "afternoon_open"
	default:
		return "afternoon_close"
	}
}

type preMarketSessionMeta struct {
	session      string
	emoji        string
	titleLabel   string
	sectionLabel string
	aiLabel      string
	aiRole       string
	aiMaxWords   string
	calendar     bool
	triggers     bool
}

func preMarketMeta(session string) preMarketSessionMeta {
	switch session {
	case "morning_close":
		return preMarketSessionMeta{
			session: "morning_close", emoji: "🌆", titleLabel: "上午收盘",
			sectionLabel: "实时行情", aiLabel: "AI 午间点评",
			aiRole: "A 股午间分析师", aiMaxWords: "200",
			calendar: false, triggers: false,
		}
	case "afternoon_open":
		return preMarketSessionMeta{
			session: "afternoon_open", emoji: "🏙", titleLabel: "午后开盘",
			sectionLabel: "实时行情", aiLabel: "AI 午后研判",
			aiRole: "A 股午后分析师", aiMaxWords: "200",
			calendar: false, triggers: false,
		}
	case "afternoon_close":
		return preMarketSessionMeta{
			session: "afternoon_close", emoji: "🌇", titleLabel: "收盘行情",
			sectionLabel: "实时行情", aiLabel: "AI 收盘点评",
			aiRole: "A 股收盘分析师", aiMaxWords: "200",
			calendar: false, triggers: false,
		}
	default: // pre_market
		return preMarketSessionMeta{
			session: "pre_market", emoji: "🌅", titleLabel: "盘前全景",
			sectionLabel: "隔夜行情", aiLabel: "AI 盘前研判",
			aiRole: "A 股盘前分析师", aiMaxWords: "200",
			calendar: true, triggers: true,
		}
	}
}

func (s *MarketBriefingSource) fetchPreMarket(parent context.Context) ([]*model.Message, error) {
	if !IsAShareBroadlyActive(time.Now()) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
	defer cancel()

	now := time.Now()
	session := preMarketSession(now)
	meta := preMarketMeta(session)

	// 1. 行情 quotes（海外 + 商品 + A股大盘）
	overseasSymbols := []string{"SPY", "QQQ", "^VIX", "^HSI", "^HSTECH", "CL=F", "GC=F", "SI=F", "BTC-USD", "000001.SS", "399001.SZ", "399006.SZ"}
	overseasData := s.fetchQuotesBlock(ctx, overseasSymbols)

	// 2. 隔夜异动触发（仅盘前时段）
	var triggerSignals []overseasSignal
	if meta.triggers {
		triggerSignals = s.overseasTriggerCheck(ctx)
	}

	// 3. 日历脚本（仅盘前时段）
	var earnings []earningsCalendarItem
	var ipo []ipoCalendarItem
	var hkIpo []ipoCalendarItem
	var unlock []unlockCalendarItem
	if meta.calendar {
		specs := []ScriptSpec{
			{Key: "earnings", Cmd: s.pythonCmd,
				Args: []string{s.scriptsDir + "/earnings_calendar.py", "--json-only"},
				Timeout: 90 * time.Second, Optional: true},
			{Key: "ipo", Cmd: s.pythonCmd,
				Args: []string{s.scriptsDir + "/ipo_calendar.py", "--json-only"},
				Timeout: 60 * time.Second, Optional: true},
			{Key: "hk_ipo", Cmd: s.pythonCmd,
				Args: []string{s.scriptsDir + "/hk_ipo_calendar.py", "--json-only"},
				Timeout: 60 * time.Second, Optional: true},
			{Key: "unlock", Cmd: s.pythonCmd,
				Args: []string{s.scriptsDir + "/unlock_calendar.py", "--json-only"},
				Timeout: 60 * time.Second, Optional: true},
		}
		results := RunScripts(ctx, SetSpecsSourceName(specs, s.name))
		_ = GetJSON(results, "earnings", &earnings)
		_ = GetJSON(results, "ipo", &ipo)
		_ = GetJSON(results, "hk_ipo", &hkIpo)
		_ = GetJSON(results, "unlock", &unlock)
	}

	goldSilverLine := s.fetchGoldSilverRatio(ctx)
	forexLines := s.fetchForexLines(ctx)

	// 4. AI 叙事
	narrative := s.preMarketNarrative(ctx, session, overseasData, earnings, ipo, hkIpo, unlock)
	if len(overseasData) == 0 && len(triggerSignals) == 0 && len(earnings) == 0 &&
		len(ipo) == 0 && len(hkIpo) == 0 && len(unlock) == 0 && strings.TrimSpace(narrative) == "" {
		return nil, nil
	}

	// 5. 渲染
	content := s.renderPreMarket(meta, overseasData, triggerSignals, earnings, ipo, hkIpo, unlock, goldSilverLine, forexLines, narrative)
	// ID 含运行时刻 HHMM:冷启动 catch-up 补跑旧槽时 session 由 time.Now() 判定会漂移,
	// 若只用 session+日期,补跑消息会与后续真实槽位 ID 撞车被 Stage1 去重误杀;
	// 带 HHMM 后补跑与真实槽各得唯一 ID(补跑消息标签可能偏差,但真实推送不被吞)。
	id := fmt.Sprintf("pre_market_%s_%s_%s", session, now.Format("20060102"), now.Format("1504"))
	tags := []string{"A股"}
	if meta.calendar {
		tags = append(tags, "盘前", "全景", "日历")
	} else {
		tags = append(tags, meta.titleLabel)
	}
	msg := &model.Message{
		Type:       model.TypeAnalysis,
		ID:         id,
		Title:      fmt.Sprintf("%s %s · %s", meta.emoji, meta.titleLabel, now.Format("01-02")),
		Content:    content,
		Source:     s.name,
		SourceType: "market-briefing",
		CreateTime: now,
		FetchTime:  now,
		Tags:       tags,
	}
	msg.SetMetadata("display_source", meta.titleLabel)
	return []*model.Message{msg}, nil
}

func (s *MarketBriefingSource) renderPreMarket(
	meta preMarketSessionMeta,
	overseas []string,
	triggerSignals []overseasSignal,
	earnings []earningsCalendarItem,
	ipo []ipoCalendarItem,
	hkIpo []ipoCalendarItem,
	unlock []unlockCalendarItem,
	goldSilverLine string,
	forexLines []string,
	narrative string,
) string {
	var b strings.Builder

	// 行情块（隔夜/实时）
	b.WriteString(fmt.Sprintf("── %s ──\n", meta.sectionLabel))
	if len(overseas) > 0 {
		for _, line := range overseas {
			b.WriteString(line + "\n")
		}
	} else {
		b.WriteString("暂无数据\n")
	}
	b.WriteString("\n")

	// 隔夜异动触发（仅盘前有数据,自然过滤）
	if len(triggerSignals) > 0 {
		b.WriteString("── 隔夜异动触发 ──\n")
		for _, sig := range triggerSignals {
			icon := "🟢"
			if sig.ChangePct < 0 {
				icon = "🔴"
			}
			b.WriteString(fmt.Sprintf("%s %s %+.2f%% → %s\n",
				icon, sig.Ticker, sig.ChangePct, strings.Join(sig.Sectors, ", ")))
		}
		b.WriteString("\n")
	}

	// 金银比 + 汇率

	if goldSilverLine != "" {
		b.WriteString(goldSilverLine + "\n")
	}
	if len(forexLines) > 0 {
		for _, l := range forexLines {
			b.WriteString(l + "\n")
		}
	}
	b.WriteString("\n")

	// 今日日历（仅盘前时段）
	if meta.calendar {
		b.WriteString("── 今日日历 ──\n")

		if len(earnings) > 0 {
			b.WriteString("📋 业绩披露:\n")
			for i, e := range earnings {
				if i >= 5 {
					b.WriteString(fmt.Sprintf("  ... 共 %d 家\n", len(earnings)))
					break
				}
				b.WriteString(fmt.Sprintf("  · %s %s\n", e.Code, e.Name))
			}
		}

		if len(ipo) > 0 {
			b.WriteString("🆕 A股IPO:\n")
			for i, p := range ipo {
				if i >= 3 {
					b.WriteString(fmt.Sprintf("  ... 共 %d 只\n", len(ipo)))
					break
				}
				b.WriteString(fmt.Sprintf("  · %s %s (发行价 %.2f)\n", p.Code, p.Name, p.Price))
			}
		}
		if len(hkIpo) > 0 {
			b.WriteString("🆕 港股IPO:\n")
			for i, p := range hkIpo {
				if i >= 3 {
					b.WriteString(fmt.Sprintf("  ... 共 %d 只\n", len(hkIpo)))
					break
				}
				b.WriteString(fmt.Sprintf("  · %s %s (发行价 %.2f)\n", p.Code, p.Name, p.Price))
			}
		}

		if len(unlock) > 0 {
			b.WriteString("🔓 解禁提醒:\n")
			for i, u := range unlock {
				if i >= 5 {
					b.WriteString(fmt.Sprintf("  ... 共 %d 只\n", len(unlock)))
					break
				}
				b.WriteString(fmt.Sprintf("  · %s %s (%.2f万股)\n", u.Code, u.Name, u.Shares))
			}
		}
		b.WriteString("\n")
	}

	if narrative != "" {
		b.WriteString(FormatLLMSection("🤖 "+meta.aiLabel, narrative))
	}
	return b.String()
}

func (s *MarketBriefingSource) preMarketNarrative(
	ctx context.Context,
	session string,
	overseas []string,
	earnings []earningsCalendarItem,
	ipo []ipoCalendarItem,
	hkIpo []ipoCalendarItem,
	unlock []unlockCalendarItem,
) string {
	if s.llmProvider == nil {
		return ""
	}

	meta := preMarketMeta(session)

	var lines []string
	if session == "pre_market" {
		lines = append(lines, "【隔夜海外行情】")
	} else {
		lines = append(lines, "【实时行情】")
	}
	lines = append(lines, overseas...)

	if session == "pre_market" {
		if len(earnings) > 0 {
			names := make([]string, 0, len(earnings))
			for _, e := range earnings {
				names = append(names, e.Name)
			}
			lines = append(lines, fmt.Sprintf("【今日业绩披露】%s", strings.Join(names, ", ")))
		}
		if len(ipo) > 0 {
			names := make([]string, 0, len(ipo))
			for _, p := range ipo {
				names = append(names, p.Name)
			}
			lines = append(lines, fmt.Sprintf("【A股IPO】%s", strings.Join(names, ", ")))
		}
		if len(hkIpo) > 0 {
			names := make([]string, 0, len(hkIpo))
			for _, p := range hkIpo {
				names = append(names, p.Name)
			}
			lines = append(lines, fmt.Sprintf("【港股IPO】%s", strings.Join(names, ", ")))
		}
		if len(unlock) > 0 {
			names := make([]string, 0, len(unlock))
			for _, u := range unlock {
				names = append(names, u.Name)
			}
			lines = append(lines, fmt.Sprintf("【解禁提醒】%s", strings.Join(names, ", ")))
		}
	}

	var prompt string
	if session == "pre_market" {
		prompt = fmt.Sprintf(`你是资深 A 股盘前分析师，请基于以下数据做 %s 字以内盘前研判:

%s

要求:
1) 隔夜海外对 A 股的影响判断 (利好/中性/压力)
2) 今日关键日历事件对大盘的影响
3) 今日重点关注方向和风险提示
4) 直接输出，不要前置寒暄。`, meta.aiMaxWords, strings.Join(lines, "\n"))
	} else {
		prompt = fmt.Sprintf(`你是资深%s，请基于以下实时行情数据做%s字以内%s:

%s

要求:
1) 当前行情概览与主要走向
2) 板块与风格特征
3) 下午/次日关键观察和风险提示
4) 直接输出，不要前置寒暄。`, meta.aiRole, meta.aiMaxWords, meta.aiLabel, strings.Join(lines, "\n"))
	}

	return LLMSummarizeWithSource(ctx, s.llmProvider, s.name, prompt, "thinking", 40*time.Second)
}

// ── closing: 收盘总览 (16:10) ──
// 市场总览 + 板块轮动 + 资金面 + 龙虎榜 + 情绪 + AI 复盘

func (s *MarketBriefingSource) fetchClosing(parent context.Context) ([]*model.Message, error) {
	if !IsAShareBroadlyActive(time.Now()) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()

	specs := []ScriptSpec{
		{Key: "sentiment", Cmd: s.pythonCmd,
			Args: []string{s.scriptsDir + "/market_sentiment.py", "--json-only"},
			Timeout: 2 * time.Minute, Optional: true},
		{Key: "hot_sector", Cmd: s.pythonCmd,
			Args: []string{s.scriptsDir + "/hot_sector_push.py", "--json-only", "--top", "5"},
			Timeout: 3 * time.Minute, Optional: true},
		{Key: "capital_flow", Cmd: s.pythonCmd,
			Args: []string{s.scriptsDir + "/capital_flow.py", "--json-only"},
			Timeout: 2 * time.Minute, Optional: true},
		{Key: "lhb", Cmd: s.pythonCmd,
			Args: []string{s.scriptsDir + "/lhb.py", "--json-only"},
			Timeout: 2 * time.Minute, Optional: true},
		{Key: "closing_scan", Cmd: s.pythonCmd,
			Args: []string{s.scriptsDir + "/closing_scan.py", "--json-only"},
			Timeout: 3 * time.Minute, Optional: true},
	}
	results := RunScripts(ctx, SetSpecsSourceName(specs, s.name))

	var sentiment marketSentimentReport
	var hotSector hotSectorEveningReport
	var capFlow capitalFlowEveningReport
	var lhb lhbEveningReport

	_ = GetJSON(results, "sentiment", &sentiment)
	_ = GetJSON(results, "hot_sector", &hotSector)
	_ = GetJSON(results, "capital_flow", &capFlow)
	_ = GetJSON(results, "lhb", &lhb)

	var closingScan closingScanReport
	closingScanUnavailable := false
	if err := GetJSON(results, "closing_scan", &closingScan); err != nil {
		closingScanUnavailable = true
		slog.Warn("closing scan data unavailable", "source", s.name, "error", err)
	}

	// AI 收盘叙事
	narrative := s.closingNarrative(ctx, &sentiment, &hotSector, &lhb, &capFlow, &closingScan)
	if len(sentiment.Indices) == 0 && sentiment.Breadth.UpCount == 0 &&
		sentiment.Breadth.DownCount == 0 && sentiment.Volume.Total == 0 &&
		sentiment.Sentiment.Level == "" && len(hotSector.Sectors) == 0 &&
		len(lhb.Stocks) == 0 && capFlow.Margin == nil && len(capFlow.MainCapitalInflow) == 0 &&
		len(closingScan.Candidates) == 0 && !closingScanUnavailable && strings.TrimSpace(narrative) == "" {
		return nil, nil
	}

	content := s.renderClosing(&sentiment, &hotSector, &lhb, &capFlow, &closingScan, closingScanUnavailable, narrative)
	now := time.Now()
	id := fmt.Sprintf("closing_briefing_%s", now.Format("20060102"))
	msg := &model.Message{
		Type:       model.TypeAnalysis,
		ID:         id,
		Title:      fmt.Sprintf("📊 收盘总览 · %s", now.Format("01-02")),
		Content:    content,
		Source:     s.name,
		SourceType: "market-briefing",
		CreateTime: now,
		FetchTime:  now,
		Tags:       []string{"A股", "收盘", "总览", "复盘"},
	}
	msg.SetMetadata("display_source", "收盘总览")
	return []*model.Message{msg}, nil
}

func (s *MarketBriefingSource) renderClosing(
	sen *marketSentimentReport,
	hs *hotSectorEveningReport,
	lhb *lhbEveningReport,
	cf *capitalFlowEveningReport,
	closingScan *closingScanReport,
	closingScanUnavailable bool,
	narrative string,
) string {
	var b strings.Builder

	// 市场总览
	b.WriteString("── 市场总览 ──\n")
	if len(sen.Indices) > 0 {
		parts := make([]string, 0, len(sen.Indices))
		for _, ix := range sen.Indices {
			parts = append(parts, fmt.Sprintf("%s %+.2f%%", ix.Name, ix.ChangePct))
		}
		b.WriteString("指数: " + strings.Join(parts, " / ") + "\n")
	}
	if sen.Breadth.UpCount > 0 || sen.Breadth.DownCount > 0 {
		b.WriteString(fmt.Sprintf("涨跌比: %d / %d | 涨停 %d / 跌停 %d | 炸板率 %.0f%%\n",
			sen.Breadth.UpCount, sen.Breadth.DownCount,
			sen.Limits.ZtCount, sen.Limits.DtCount, sen.Limits.ZbRate*100))
	}
	if sen.Volume.Total > 0 {
		b.WriteString(fmt.Sprintf("成交额: %.0f亿 (%+.1f%%)\n", sen.Volume.Total/1e8, sen.Volume.ChangePct))
	}
	if sen.Sentiment.Level != "" {
		b.WriteString(fmt.Sprintf("情绪: %s (%.1f)\n", sen.Sentiment.Level, sen.Sentiment.Score))
	}
	b.WriteString("\n")

	// 热门板块
	if len(hs.Sectors) > 0 {
		b.WriteString("── 板块轮动 ──\n")
		for i, sec := range hs.Sectors {
			if i >= 5 {
				break
			}
			b.WriteString(fmt.Sprintf("🔥 %s (+%.2f%%)\n", sec.Name, sec.ChangePct))
			for j, l := range sec.Leaders {
				if j >= 2 {
					break
				}
				b.WriteString(fmt.Sprintf("   · [%s] %s %+.2f%%\n", l.Code, l.Name, l.ChangePct))
			}
		}
		b.WriteString("\n")
	}

	// 资金面
	hasMargin := cf.Margin != nil && cf.Margin.MarginBalance > 0
	if hasMargin || len(cf.MainCapitalInflow) > 0 {
		b.WriteString("── 资金面 ──\n")
		if hasMargin {
			b.WriteString(fmt.Sprintf("两融余额: %.0f亿 (%+.2f%%)\n",
				cf.Margin.MarginBalance, cf.Margin.TotalChangePct))
		}
		if len(cf.MainCapitalInflow) > 0 {
			b.WriteString("主力净流入 Top5:\n")
			for i, c := range cf.MainCapitalInflow {
				if i >= 5 {
					break
				}
				b.WriteString(fmt.Sprintf("   · [%s] %s +%.1f亿\n", c.Code, c.Name, c.NetInflow))
			}
		}
		b.WriteString("\n")
	}

	// 龙虎榜
	if len(lhb.Stocks) > 0 {
		b.WriteString("── 龙虎榜 ──\n")
		for i, st := range lhb.Stocks {
			if i >= 5 {
				break
			}
			b.WriteString(fmt.Sprintf("• [%s] %s %+.2f%% 净%s%.1f亿",
				st.Code, st.Name, st.ChangePct, netSign(st.NetAmount), st.NetAmount/1e8))
			if st.Reason != "" {
				b.WriteString(" · " + st.Reason)
			}
			b.WriteString("\n")
		}
		if len(lhb.Institution.BuyStocks) > 0 {
			b.WriteString("机构买入: ")
			parts := make([]string, 0, len(lhb.Institution.BuyStocks))
			for i, st := range lhb.Institution.BuyStocks {
				if i >= 3 {
					break
				}
				parts = append(parts, st.Name)
			}
			b.WriteString(strings.Join(parts, ", ") + "\n")
		}
		b.WriteString("\n")
	}

	// closing_scan KNN 预测。只展示类型化、有界摘要，绝不嵌入脚本原文。
	if closingScanUnavailable || closingScan != nil && len(closingScan.Candidates) > 0 {
		b.WriteString("── 打板预测 ──\n")
		if closingScanUnavailable {
			b.WriteString("预测数据暂不可用\n\n")
		} else {
			b.WriteString(formatClosingScanSummary(closingScan, 5) + "\n\n")
		}
	}

	if narrative != "" {
		b.WriteString(FormatLLMSection("🤖 AI 复盘", narrative))
	}
	return b.String()
}

func (s *MarketBriefingSource) closingNarrative(
	ctx context.Context,
	sen *marketSentimentReport,
	hs *hotSectorEveningReport,
	lhb *lhbEveningReport,
	cf *capitalFlowEveningReport,
	closingScan *closingScanReport,
) string {
	if s.llmProvider == nil {
		return ""
	}

	var lines []string

	if len(sen.Indices) > 0 {
		idx := make([]string, 0, len(sen.Indices))
		for _, ix := range sen.Indices {
			idx = append(idx, fmt.Sprintf("%s %+.2f%%", ix.Name, ix.ChangePct))
		}
		lines = append(lines, "指数: "+strings.Join(idx, " / "))
	}
	if sen.Breadth.UpCount > 0 || sen.Breadth.DownCount > 0 {
		lines = append(lines, fmt.Sprintf("涨跌: %d/%d, 涨停 %d / 跌停 %d, 炸板率 %.0f%%",
			sen.Breadth.UpCount, sen.Breadth.DownCount,
			sen.Limits.ZtCount, sen.Limits.DtCount, sen.Limits.ZbRate*100))
	}
	if sen.Sentiment.Level != "" {
		lines = append(lines, fmt.Sprintf("情绪: %s (%.1f)", sen.Sentiment.Level, sen.Sentiment.Score))
	}
	if len(hs.Sectors) > 0 {
		topSec := make([]string, 0, 3)
		for i, sec := range hs.Sectors {
			if i >= 3 {
				break
			}
			topSec = append(topSec, fmt.Sprintf("%s(%+.2f%%)", sec.Name, sec.ChangePct))
		}
		lines = append(lines, "热门板块: "+strings.Join(topSec, ", "))
	}
	if lhb != nil && len(lhb.Stocks) > 0 {
		lines = append(lines, fmt.Sprintf("龙虎榜 Top: %s (净%s%.1f亿)",
			lhb.Stocks[0].Name, netSign(lhb.Stocks[0].NetAmount), lhb.Stocks[0].NetAmount/1e8))
	}
	if cf != nil && cf.Margin != nil && cf.Margin.MarginBalance > 0 {
		lines = append(lines, fmt.Sprintf("两融 %.0f亿 (%+.2f%%)",
			cf.Margin.MarginBalance, cf.Margin.TotalChangePct))
	}
	if summary := formatClosingScanSummary(closingScan, 5); summary != "" {
		lines = append(lines, "打板预测:\n"+summary)
	}

	if len(lines) == 0 {
		return ""
	}

	prompt := fmt.Sprintf(`你是资深 A 股分析师，请基于今日收盘数据做一份 300 字以内收盘复盘:

%s

要求:
1) 概括今日主要走势与风格特征 (1-2 句话)
2) 板块轮动方向: 领涨板块是否有延续性，领跌板块是否有反弹基础
3) 资金面分析: 机构/游资/散户情绪，两融变化信号
4) 龙虎榜活跃度及机构动向
5) 明日关键观察和风险提示
6) 直接输出，不要前置寒暄。`, strings.Join(lines, "\n"))

	return LLMSummarizeWithSource(ctx, s.llmProvider, s.name, prompt, "thinking", 60*time.Second)
}

// ── aggregate: 新闻聚合简报 (早报/晚报) ──
// digest news_aggregate 桶 + BlockBeats 脚本(morning/evening)合并推送。

func (s *MarketBriefingSource) fetchAggregate(parent context.Context) ([]*model.Message, error) {
	mode := s.blockbeatsMode
	if mode == "" {
		mode = "evening"
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
	defer cancel()

	// 1. BlockBeats 脚本(morning: 早报 / evening: 晚报)
	specs := []ScriptSpec{
		{Key: "blockbeats", Cmd: s.pythonCmd,
			Args: []string{s.scriptsDir + "/blockbeats_daily_reports.py", "--mode", mode},
			Timeout: 120 * time.Second, Optional: true},
	}
	results := RunScripts(ctx, SetSpecsSourceName(specs, s.name))
	blockbeatsContent := ""
	if r, ok := results["blockbeats"]; ok && len(r.Raw) > 0 {
		// RunScripts 对非 JSON 输出置 Err(JSON 解析失败)——纯文本脚本属正常;
		// 仅"parse json"错误可忽略,其余(超时/非零退出)丢弃可能含 traceback 的输出。
		if r.Err != nil && !strings.Contains(r.Err.Error(), "parse json") {
			slog.Warn("blockbeats script failed", "source", s.name, "error", r.Err)
		} else {
			blockbeatsContent = strings.TrimSpace(string(r.Raw))
			// 剥掉 BlockBeats 自带首行标题(与聚合标题重复,如 "🌆 BlockBeats 晚报…")
			if idx := strings.IndexByte(blockbeatsContent, '\n'); idx >= 0 {
				blockbeatsContent = strings.TrimSpace(blockbeatsContent[idx+1:])
			}
		}
	}

	// 2. 租 digest news_aggregate 桶
	var summary string
	var leaseID string
	if s.digestStore != nil {
		var lease *digest.Lease
		var err error
		if topicStore, ok := s.digestStore.(digest.TopicStore); ok {
			lease, err = topicStore.LeaseTopics(ctx, digest.NewsAggregate, digestBriefingItemLimit, 30*time.Minute)
		} else {
			lease, err = s.digestStore.Lease(ctx, digest.NewsAggregate, digestBriefingItemLimit, 30*time.Minute)
		}
		if err != nil {
			return nil, fmt.Errorf("lease digest: %w", err)
		}
		if lease != nil {
			summary = digest.RenderTopics(lease.Topics, digestBriefingItemLimit)
			leaseID = lease.ID
		}
	}

	// 3. 拼装
	if blockbeatsContent == "" && summary == "" {
		return nil, nil
	}
	now := time.Now()
	header, label := "🌆", "晚报"
	if mode == "morning" {
		header, label = "🌅", "早报"
	}
	var b strings.Builder
	if summary != "" {
		b.WriteString("## 资讯摘要\n" + summary + "\n\n")
	}
	if blockbeatsContent != "" {
		b.WriteString(blockbeatsContent)
	}
	msg := &model.Message{
		Type:       model.TypeAnalysis,
		ID:         fmt.Sprintf("news_aggregate_%s_%s", mode, now.Format("20060102")),
		Title:      fmt.Sprintf("%s 新闻聚合 · %s", header, now.Format("01-02")),
		Content:    b.String(),
		Source:     s.name,
		SourceType: "market-briefing",
		CreateTime: now,
		FetchTime:  now,
		Tags:       []string{"新闻", "聚合", label},
	}
	// 租到 digest 桶则走 durable digest 投递(与 us-preview 相同路径)
	if leaseID != "" {
		msg.SetMetadata("digest_lease_id", leaseID)
		msg.SetMetadata("digest_briefing", string(digest.NewsAggregate))
	}
	msg.SetMetadata("display_source", label)
	return []*model.Message{msg}, nil
}

// ── us_preview: 美盘前瞻 (21:10) ──
// fedwatch + VIX + 关键标的行情 + AI 叙事

func (s *MarketBriefingSource) fetchUSPreview(parent context.Context) ([]*model.Message, error) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
	defer cancel()

	// 1. 宏观脚本
	specs := []ScriptSpec{
		{Key: "fedwatch", Cmd: s.pythonCmd,
			Args: []string{s.scriptsDir + "/fedwatch.py", "--json-only"},
			Timeout: 90 * time.Second, Optional: true},
		{Key: "vix_term", Cmd: s.pythonCmd,
			Args: []string{s.scriptsDir + "/vix_term.py", "--json-only"},
			Timeout: 60 * time.Second, Optional: true},
	}
	results := RunScripts(ctx, SetSpecsSourceName(specs, s.name))

	var fedwatchRaw string
	var vixTermRaw string
	if r, ok := results["fedwatch"]; ok && r.Err == nil {
		fedwatchRaw = string(r.Raw)
	}
	if r, ok := results["vix_term"]; ok && r.Err == nil {
		vixTermRaw = string(r.Raw)
	}
	// 2. 关键标的行情
	ussymbols := []string{"SPY", "QQQ", "GC=F", "CL=F", "BTC-USD"}
	quoteLines := s.fetchQuotesBlock(ctx, ussymbols)

	// 3. Nasdaq 美股事件日历。该依赖是可选的，单端点/整体失败均不阻断简报。
	calendar := s.fetchNasdaqCalendar(ctx, time.Now())





	// 4. AI 叙事
	narrative := s.usPreviewNarrative(ctx, quoteLines, fedwatchRaw, vixTermRaw)
	if len(quoteLines) == 0 && strings.TrimSpace(fedwatchRaw) == "" &&
		strings.TrimSpace(vixTermRaw) == "" && calendar == "" && strings.TrimSpace(narrative) == "" {
		return nil, nil
	}

	content := s.renderUSPreview(quoteLines, fedwatchRaw, vixTermRaw, calendar, narrative)
	now := time.Now()
	id := fmt.Sprintf("us_preview_%s", now.Format("20060102"))
	msg := &model.Message{
		Type:       model.TypeAnalysis,
		ID:         id,
		Title:      fmt.Sprintf("🌎 美盘前瞻 · %s", now.Format("01-02")),
		Content:    content,
		Source:     s.name,
		SourceType: "market-briefing",
		CreateTime: now,
		FetchTime:  now,
		Tags:       []string{"美股", "盘前", "宏观", "前瞻"},
	}
	msg.SetMetadata("display_source", "美盘前瞻")
	return []*model.Message{msg}, nil
}

func (s *MarketBriefingSource) renderUSPreview(
	quotes []string,
	fedwatchRaw string,
	vixTermRaw string,
	calendar string,
	narrative string,
) string {
	var b strings.Builder

	// 关键标的行情
	b.WriteString("── 关键标的 ──\n")
	if len(quotes) > 0 {
		for _, line := range quotes {
			b.WriteString(line + "\n")
		}
	} else {
		b.WriteString("暂无数据\n")
	}
	b.WriteString("\n")

	// FedWatch 利率预期 (formatted for chat)
	if fw := formatFedWatch(fedwatchRaw); fw != "" {
		b.WriteString("── 利率路径 ──\n")
		b.WriteString(fw + "\n\n")
	}

	// VIX 期限结构 (formatted for chat)
	if vt := formatVIXTerm(vixTermRaw); vt != "" {
		b.WriteString("── VIX ──\n")
		b.WriteString(vt + "\n\n")
	}

	if calendar != "" {
		b.WriteString(calendar)
		b.WriteString("\n\n")
	}

	if narrative != "" {
		b.WriteString(FormatLLMSection("🤖 AI 前瞻研判", narrative))
	}
	return b.String()
}

func (s *MarketBriefingSource) fetchNasdaqCalendar(parent context.Context, now time.Time) string {
	if !s.nasdaqEnabled || s.nasdaqClient == nil || s.briefingType != "us_preview" {
		return ""
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	events, err := s.nasdaqClient.FetchCalendar(ctx, nasdaqCalendarDate(now))
	if err != nil {
		slog.Warn("nasdaq calendar partial failure", "source", s.name, "error", err, "events", len(events))
	}
	return renderNasdaqCalendar(events)
}

func nasdaqCalendarDate(now time.Time) string {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		location = time.FixedZone("America/New_York", -5*60*60)
	}
	return now.In(location).Format("2006-01-02")
}

func renderNasdaqCalendar(events []nasdaq.Event) string {
	var earnings, dividends, splits []nasdaq.Event
	var cachedFallback bool
	var cachedAt time.Time
	for _, event := range events {
		if event.DataStatus == nasdaq.EndpointCachedFallback {
			cachedFallback = true
			if event.CachedAt != nil && (cachedAt.IsZero() || event.CachedAt.Before(cachedAt)) {
				cachedAt = *event.CachedAt
			}
		}
		switch event.Type {
		case nasdaq.EventEarnings:
			earnings = append(earnings, event)
		case nasdaq.EventDividend:
			if event.Amount != nil && *event.Amount > 0 {
				dividends = append(dividends, event)
			}
		case nasdaq.EventSplit:
			if event.SplitRatio != nil && event.SplitRatio.Numerator > 0 &&
				event.SplitRatio.Denominator > 0 &&
				event.SplitRatio.Numerator != event.SplitRatio.Denominator {
				splits = append(splits, event)
			}
		}
	}
	if len(earnings) == 0 && len(dividends) == 0 && len(splits) == 0 {
		return ""
	}
	sessionRank := func(session nasdaq.Session) int {
		switch session {
		case nasdaq.SessionBeforeMarket:
			return 0
		case nasdaq.SessionAfterMarket:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(earnings, func(i, j int) bool {
		if sessionRank(earnings[i].Session) != sessionRank(earnings[j].Session) {
			return sessionRank(earnings[i].Session) < sessionRank(earnings[j].Session)
		}
		return earnings[i].Symbol < earnings[j].Symbol
	})
	sort.SliceStable(dividends, func(i, j int) bool { return dividends[i].Symbol < dividends[j].Symbol })
	sort.SliceStable(splits, func(i, j int) bool { return splits[i].Symbol < splits[j].Symbol })

	var b strings.Builder
	b.WriteString("── 美股事件日历 ──\n")
	if cachedFallback {
		b.WriteString("⚠️ Cached fallback · Nasdaq 实时请求失败，以下为同一交易日缓存")
		if !cachedAt.IsZero() {
			b.WriteString("（缓存于 " + cachedAt.Format("2006-01-02 15:04 UTC") + "）")
		}
		b.WriteString("\n")
	}
	if len(earnings) > 0 {
		b.WriteString("业绩:\n")
		provenance := ""
		for i, event := range earnings {
			if i >= 6 {
				break
			}
			session := "时段待定"
			switch event.Session {
			case nasdaq.SessionBeforeMarket:
				session = "盘前"
			case nasdaq.SessionAfterMarket:
				session = "盘后"
			}
			b.WriteString(fmt.Sprintf("  · %s %s", session, strings.TrimPrefix(event.Symbol, "US:")))
			if event.EPS != nil {
				b.WriteString(fmt.Sprintf(" EPS预期 %.2f", *event.EPS))
			}
			b.WriteString("\n")
			if event.Estimated && provenance == "" {
				provenance = event.Provenance
			}
		}
		if provenance != "" {
			b.WriteString("  ⚠️ Estimated · " + provenance + "\n")
		}
	}
	if len(dividends) > 0 {
		b.WriteString("除息:")
		for i, event := range dividends {
			if i >= 3 {
				break
			}
			b.WriteString(fmt.Sprintf(" %s $%.3g", strings.TrimPrefix(event.Symbol, "US:"), *event.Amount))
		}
		b.WriteString("\n")
	}
	if len(splits) > 0 {
		b.WriteString("拆股:")
		for i, event := range splits {
			if i >= 2 {
				break
			}
			b.WriteString(fmt.Sprintf(" %s %.3g:%.3g", strings.TrimPrefix(event.Symbol, "US:"),
				event.SplitRatio.Numerator, event.SplitRatio.Denominator))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func (s *MarketBriefingSource) usPreviewNarrative(
	ctx context.Context,
	quotes []string,
	fedwatchRaw string,
	vixTermRaw string,
) string {
	if s.llmProvider == nil {
		return ""
	}

	var lines []string
	lines = append(lines, "【关键标的行情】")
	lines = append(lines, quotes...)

	if fedwatchRaw != "" {
		lines = append(lines, fmt.Sprintf("【FedWatch利率预期】%s", truncateStr(fedwatchRaw, 600)))
	}
	if vixTermRaw != "" {
		lines = append(lines, fmt.Sprintf("【VIX期限结构】%s", truncateStr(vixTermRaw, 600)))
	}

	prompt := fmt.Sprintf(`你是资深美股盘前分析师，请基于以下数据做 250 字以内美盘前瞻研判:

%s

要求:
1) 盘前核心矛盾与驱动因素
2) 利率预期对市场的影响（科技/价值/防御风格轮动）
3) VIX 反映的市场恐慌/贪婪信号
4) 今晚关键风险/事件关注
5) 直接输出，不要前置寒暄。`, strings.Join(lines, "\n"))

	return LLMSummarizeWithSource(ctx, s.llmProvider, s.name+"-us", prompt, "thinking", 50*time.Second)
}

// ── 工具方法 ──

// fetchQuotesBlock 批量获取行情，返回格式化行
func (s *MarketBriefingSource) fetchQuotesBlock(ctx context.Context, symbols []string) []string {
	// 并发拉取：串行时末位符号(如 BTC-USD)会因共享 ctx 超时被饿死。
	// 结果按下标回填,保持符号顺序稳定。
	lines := make([]string, len(symbols))
	var wg sync.WaitGroup
	for i, sym := range symbols {
		wg.Add(1)
		go func(i int, sym string) {
			defer wg.Done()
			q, err := s.marketService.GetMarketQuote(ctx, sym)
			if err != nil || q == nil || q.Price == 0 {
				return
			}

			icon := "🔹"
			if q.ChangePercent > 0 {
				icon = "🟢"
			} else if q.ChangePercent < 0 {
				icon = "🔴"
			}

			name := q.Name
			if name == "" {
				name = sym
			}
			if len([]rune(name)) > 12 {
				name = string([]rune(name)[:12])
			}

			lines[i] = fmt.Sprintf("%s %s: %.2f (%+.2f%%)", icon, name, q.Price, q.ChangePercent)
		}(i, sym)
	}
	wg.Wait()

	var out []string
	for _, line := range lines {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// truncateStr 截断字符串到 maxLen 字节，避免日志炸弹
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// overseasTriggerCheck 检测隔夜美股触发异动，返回超过阈值的信号列表
func (s *MarketBriefingSource) overseasTriggerCheck(ctx context.Context) []overseasSignal {
	var signals []overseasSignal
	for ticker, sectors := range us2cnSector {
		q, err := s.marketService.GetMarketQuote(ctx, ticker)
		if err != nil || q == nil {
			continue
		}
		if abs(q.ChangePercent) >= 5.0 {
			signals = append(signals, overseasSignal{
				Ticker:    ticker,
				ChangePct: q.ChangePercent,
				Sectors:   sectors,
			})
		}
	}
	// sort by absolute change descending
	sort.Slice(signals, func(i, j int) bool {
		return abs(signals[i].ChangePct) > abs(signals[j].ChangePct)
	})
	return signals
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// ─── JSON → human-readable formatters (for Discord / WeChat) ───

type fedwatchData struct {
	EFFR     float64 `json:"effr"`
	Meetings []struct {
		Date     string             `json:"date"`
		Implied  float64            `json:"implied"`
		DeltaBP  float64            `json:"delta_bp"`
		Probs    map[string]float64 `json:"probs"`
	} `json:"meetings"`
}

func formatFedWatch(raw string) string {
	var d fedwatchData
	if err := json.Unmarshal([]byte(raw), &d); err != nil || len(d.Meetings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("EFFR %.2f%%", d.EFFR))
	for _, m := range d.Meetings {
		// bias emoji
		bias := "hold"
		for k, v := range m.Probs {
			if m.Probs[bias] < v {
				bias = k
			}
		}
		emoji := map[string]string{"cut": "🟢", "hold": "⚪", "hike": "🔴"}[bias]
		b.WriteString(fmt.Sprintf("  %s %s %5.2f%%", emoji, m.Date[len(m.Date)-5:], m.Implied))
	}
	return b.String()
}

type vixTermData struct {
	VIX    float64            `json:"vix"`
	VIX3M  float64            `json:"vix3m"`
	Ratio  float64            `json:"ratio"`
	Regime string             `json:"regime"`
	Prices map[string]float64 `json:"prices"`
}

func formatVIXTerm(raw string) string {
	var d vixTermData
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s  ", d.Regime))
	// term structure in one line: 9D → VIX → 3M → 6M
	order := []string{"_VIX9D", "_VIX", "_VIX3M", "_VIX6M"}
	labels := map[string]string{"_VIX9D": "9D", "_VIX": "VIX", "_VIX3M": "3M", "_VIX6M": "6M"}
	for _, k := range order {
		if v, ok := d.Prices[k]; ok {
			b.WriteString(fmt.Sprintf("%s:%.1f  ", labels[k], v))
		}
	}
	b.WriteString(fmt.Sprintf("R:%.2f", d.Ratio))
	return b.String()
}

// fetchGoldSilverRatio returns a formatted gold/silver ratio line.
func (s *MarketBriefingSource) fetchGoldSilverRatio(ctx context.Context) string {
	doFetch := func(sym string) float64 {
		q, err := s.marketService.GetMarketQuote(ctx, sym)
		if err != nil || q == nil || q.Price == 0 {
			return 0
		}
		return q.Price
	}
	gold := doFetch("GC=F")
	silver := doFetch("SI=F")
	if gold <= 0 || silver <= 0 {
		return ""
	}
	ratio := gold / silver
	note := " (正常 70-85)"
	if ratio > 85 {
		note = " ⚠️ 白银相对低估"
	} else if ratio < 70 {
		note = " ⚠️ 黄金相对低估"
	}
	return fmt.Sprintf("📊 金银比: %.1f%s", ratio, note)
}

// fetchForexLines returns formatted forex rate lines.
func (s *MarketBriefingSource) fetchForexLines(ctx context.Context) []string {
	pairs := []string{"USDCNY=X", "EURCNY=X", "HKDCNY=X"}
	var lines []string
	for _, sym := range pairs {
		q, err := s.marketService.GetMarketQuote(ctx, sym)
		if err != nil || q == nil || q.Price == 0 {
			continue
		}
		label := strings.TrimSuffix(sym, "=X")
		lines = append(lines, fmt.Sprintf("  %s: %.4f", label, q.Price))
	}
	return lines
}

// ─── closing-briefing types (from deleted closing_report_types.go) ───

type closingScanReport struct {
	Type           string                 `json:"type"`
	Timestamp      string                 `json:"timestamp"`
	MarketTemp     closingScanMarketTemp  `json:"market_temp"`
	HistorySamples int                    `json:"history_samples"`
	Candidates     []closingScanCandidate `json:"candidates"`
}

type closingScanMarketTemp struct {
	LimitUpCount           int      `json:"zt_count"`
	LimitDownCount         int      `json:"dt_count"`
	BreakRatio             float64  `json:"today_break_ratio"`
	PreviousFirstBoardOpen float64  `json:"prev_day_first_board_avg_open"`
	MainTheme              []string `json:"main_theme"`
}

type closingScanCandidate struct {
	Code       string                `json:"code"`
	Name       string                `json:"name"`
	Streak     int                   `json:"streak"`
	Score      int                   `json:"score"`
	Prediction closingScanPrediction `json:"prediction"`
	Warnings   []string              `json:"warnings"`
}

type closingScanPrediction struct {
	Bucket     *closingScanPredictionStats `json:"bucket"`
	KNN        *closingScanPredictionStats `json:"knn"`
	Confidence string                      `json:"confidence"`
}

type closingScanPredictionStats struct {
	OpenMedian float64 `json:"open_median"`
	HighMedian float64 `json:"high_median"`
	WinRate    float64 `json:"win_rate"`
	Samples    int     `json:"n_samples"`
}

func formatClosingScanSummary(report *closingScanReport, limit int) string {
	if report == nil || len(report.Candidates) == 0 || limit <= 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("市场温度: 涨停 %d / 跌停 %d / 炸板率 %.0f%%",
		report.MarketTemp.LimitUpCount, report.MarketTemp.LimitDownCount, report.MarketTemp.BreakRatio*100))
	if len(report.MarketTemp.MainTheme) > 0 {
		themes := report.MarketTemp.MainTheme
		if len(themes) > 2 {
			themes = themes[:2]
		}
		b.WriteString(" / 主线 " + truncateStr(strings.Join(themes, "、"), 60))
	}
	b.WriteString("\n")
	for i, candidate := range report.Candidates {
		if i >= limit {
			break
		}
		b.WriteString(fmt.Sprintf("• [%s] %s %d板 | 评分 %d | 置信度 %s",
			truncateStr(candidate.Code, 12), truncateStr(candidate.Name, 20), candidate.Streak,
			candidate.Score, truncateStr(candidate.Prediction.Confidence, 24)))
		prediction := candidate.Prediction.Bucket
		predictionName := "分桶"
		if prediction == nil {
			prediction = candidate.Prediction.KNN
			predictionName = "KNN"
		}
		if prediction != nil {
			b.WriteString(fmt.Sprintf(" | %s次日开盘中位数 %+.1f%%、胜率 %.0f%%（%d样本）",
				predictionName, prediction.OpenMedian, prediction.WinRate*100, prediction.Samples))
		}
		if len(candidate.Warnings) > 0 {
			warnings := candidate.Warnings
			if len(warnings) > 2 {
				warnings = warnings[:2]
			}
			b.WriteString(" | 风险 " + truncateStr(strings.Join(warnings, "；"), 100))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

type marketSentimentReport struct {
	Indices []struct {
		Name      string  `json:"name"`
		Close     float64 `json:"close"`
		ChangePct float64 `json:"change_pct"`
	} `json:"indices"`
	Breadth struct {
		UpCount   int `json:"up_count"`
		DownCount int `json:"down_count"`
	} `json:"breadth"`
	Limits struct {
		ZtCount int     `json:"limit_up"`
		DtCount int     `json:"limit_down"`
		ZbRate  float64 `json:"broken_ratio"`
	} `json:"limits"`
	Volume struct {
		Total     float64 `json:"total"`
		ChangePct float64 `json:"change_pct"`
	} `json:"volume"`
	Sentiment struct {
		Score float64 `json:"score"`
		Level string  `json:"level"`
	} `json:"sentiment"`
}

type hotSectorEveningReport struct {
	Sectors []struct {
		Name      string  `json:"name"`
		Rank      int     `json:"rank"`
		ChangePct float64 `json:"change_pct"`
		Leaders   []struct {
			Code      string  `json:"code"`
			Name      string  `json:"name"`
			ChangePct float64 `json:"change_pct"`
		} `json:"leaders"`
	} `json:"sectors"`
}

type lhbEveningReport struct {
	Stocks []struct {
		Code      string  `json:"code"`
		Name      string  `json:"name"`
		ChangePct float64 `json:"change_pct"`
		NetAmount float64 `json:"net_amount"`
		Reason    string  `json:"reason"`
	} `json:"stocks"`
	Institution struct {
		BuyStocks []struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"buy_stocks"`
		SellStocks []struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"sell_stocks"`
	} `json:"institution"`
}

type capitalFlowEveningReport struct {
	Margin *struct {
		MarginBalance  float64 `json:"margin_balance"`
		TotalChangePct float64 `json:"total_change_pct"`
	} `json:"margin"`
	MainCapitalInflow []struct {
		Code      string  `json:"code"`
		Name      string  `json:"name"`
		NetInflow float64 `json:"net_inflow"`
	} `json:"main_capital_inflow"`
}

func netSign(v float64) string {
	if v >= 0 {
		return "+"
	}
	return "-"
}
