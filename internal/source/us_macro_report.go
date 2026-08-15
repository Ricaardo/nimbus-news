package source

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dsfred "github.com/Ricaardo/nimbus-os/datasources/fred"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/datasources/market"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func init() {
	Register("us-macro-report", NewUSMacroReportSource)
}

// USMacroReportSource 美国宏观 + 流动性聚合报告 (21:30)
type USMacroReportSource struct {
	name          string
	apiKey        string
	series        []string
	marketService market.Service
	llmProvider   llm.Provider
	pythonCmd     string
	scriptsDir    string
}

// SetLLMProvider 实现 LLMSettable
func (s *USMacroReportSource) SetLLMProvider(p llm.Provider) {
	s.llmProvider = p
}

// FRED 指标中文名 + 分类
type usMacroSeriesMeta struct {
	NameCN   string
	Category string
	Scale    float64
	Decimals int
	Unit     string
}

var usMacroMeta = map[string]usMacroSeriesMeta{
	"DFF":          {"联邦基金利率", "利率", 1, 2, "%"},
	"SOFR":         {"SOFR", "利率", 1, 2, "%"},
	"DGS2":         {"2年期国债", "利率", 1, 2, "%"},
	"DGS10":        {"10年期国债", "利率", 1, 2, "%"},
	"DGS30":        {"30年期国债", "利率", 1, 2, "%"},
	"MORTGAGE30US": {"30年房贷利率", "利率", 1, 2, "%"},
	"M2SL":         {"M2 货币供应", "流动性", 1e3, 2, "万亿美元"}, // 十亿美元
	"WALCL":        {"Fed 总资产", "流动性", 1e6, 2, "万亿美元"}, // 百万美元
	"RRPONTSYD":    {"RRP 逆回购", "流动性", 1e3, 3, "万亿美元"}, // 十亿美元
	"TOTRESNS":     {"银行储备金", "流动性", 1e6, 2, "万亿美元"},   // 百万美元
	"T10Y2Y":       {"10Y-2Y 利差", "利差", 1, 2, "个百分点"},
	"T10Y3M":       {"10Y-3M 利差", "利差", 1, 2, "个百分点"},
	"UNRATE":       {"失业率", "就业", 1, 2, "%"},
	"PAYEMS":       {"非农就业", "就业", 1e3, 1, "百万人"}, // 千人
	"CPIAUCSL":     {"CPI", "通胀", 1, 1, "（指数）"},
	"PCEPI":        {"PCE", "通胀", 1, 1, "（指数）"},
	"GDPC1":        {"实际 GDP", "经济", 1e3, 2, "万亿美元"}, // 十亿美元
	// ISM PMI 2026-08-02 查证:FRED 无该序列(候选 ID 全部不存在),不配置
	"DTWEXBGS":     {"美元指数（广义）", "汇率", 1, 2, "（指数）"},
	"VIXCLS":       {"VIX 恐慌指数", "市场", 1, 2, "（指数）"},
	"BAA10Y":       {"Baa-10Y 信用利差", "利差", 1, 2, "个百分点"},
	"T10YIE":       {"10Y 通胀预期", "通胀", 1, 2, "%"},
	"DCOILWTICO":   {"WTI 原油", "商品", 1, 2, "美元/桶"},
	// 巴菲特指标 = 非金融企业股权市值 / 名义 GDP(伪序列,由 fetchFREDAll 特殊计算)
	"BUFFETT_INDEX": {"巴菲特指标(市值/GDP)", "市场", 1, 1, "%"},
	"CPILFESL":      {"核心 CPI", "通胀", 1, 1, "（指数）"},
	"PCEPILFE":      {"核心 PCE", "通胀", 1, 1, "（指数）"},
	"RSXFS":         {"零售销售(实际)", "经济", 1e6, 2, "万亿美元"}, // 百万美元
	"ICSA":          {"初请失业金", "就业", 1e4, 1, "万人"},        // 千人
	// 劳动缺口 = JOLTS 职位空缺 - 失业人数(伪序列,由 fetchFREDAll 特殊计算)
	"LABOR_GAP": {"劳动缺口(空缺-失业)", "就业", 10, 1, "万人"}, // 千→万除 10
}

func NewUSMacroReportSource(cfg Config) (Source, error) {
	s := &USMacroReportSource{
		name:   cfg.Name,
		series: []string{"DFF", "SOFR", "DGS2", "DGS10", "DGS30", "M2SL", "WALCL", "RRPONTSYD", "T10Y2Y", "T10Y3M", "UNRATE", "CPIAUCSL", "PCEPI"},
	}
	if v, ok := cfg.Options["api_key"].(string); ok {
		s.apiKey = v
	}
	if s.apiKey == "" {
		return nil, fmt.Errorf("us-macro-report: api_key (FRED) required")
	}
	if v, ok := cfg.Options["series"].([]interface{}); ok {
		s.series = nil
		for _, x := range v {
			if str, ok := x.(string); ok {
				s.series = append(s.series, str)
			}
		}
	}
	if v, ok := cfg.Options["python_cmd"].(string); ok && v != "" {
		s.pythonCmd = v
	}
	if s.pythonCmd == "" {
		s.pythonCmd = "python3"
	}
	if v, ok := cfg.Options["scripts_dir"].(string); ok && v != "" {
		s.scriptsDir = v
	}
	if s.scriptsDir == "" {
		s.scriptsDir = "scripts"
	}
	s.marketService = market.NewMarketService()
	return s, nil
}

// fedWatchResp fedwatch.py --json-only 输出结构。
type fedWatchResp struct {
	EFFR     float64 `json:"effr"`
	Meetings []struct {
		Date    string  `json:"date"`
		Implied float64 `json:"implied"`
		DeltaBP float64 `json:"delta_bp"`
		Probs   struct {
			Cut  float64 `json:"cut"`
			Hold float64 `json:"hold"`
			Hike float64 `json:"hike"`
		} `json:"probs"`
	} `json:"meetings"`
}

// fetchFedWatch 运行 fedwatch.py(自建 FedWatch,30 天联邦基金期货+EFFR),
// 渲染利率路径摘要;脚本失败时静默返回空。
func (s *USMacroReportSource) fetchFedWatch(ctx context.Context) string {
	results := RunScripts(ctx, SetSpecsSourceName([]ScriptSpec{{
		Key: "fedwatch", Cmd: s.pythonCmd,
		Args:    []string{s.scriptsDir + "/fedwatch.py", "--json-only"},
		Timeout: 90 * time.Second, Optional: true,
	}}, s.name))
	r, ok := results["fedwatch"]
	if !ok || r.Err != nil || len(r.Raw) == 0 {
		return ""
	}
	var resp fedWatchResp
	if err := json.Unmarshal(r.Raw, &resp); err != nil || len(resp.Meetings) == 0 {
		slog.Warn("fedwatch parse failed", "source", s.name, "error", err)
		return ""
	}
	return renderFedWatch(resp)
}

// renderFedWatch 纯函数:EFFR + 前 4 次会议概率摘要(≥50% 定方向,否则按兵不动)。
func renderFedWatch(resp fedWatchResp) string {
	var parts []string
	for _, m := range resp.Meetings[:min(4, len(resp.Meetings))] {
		d := m.Date[5:]
		switch {
		case m.Probs.Cut >= 50:
			parts = append(parts, fmt.Sprintf("%s 降息%.0f%%", d, m.Probs.Cut))
		case m.Probs.Hike >= 50:
			parts = append(parts, fmt.Sprintf("%s 加息%.0f%%", d, m.Probs.Hike))
		default:
			parts = append(parts, fmt.Sprintf("%s 按兵不动", d))
		}
	}
	return "EFFR " + fmt.Sprintf("%.2f%%", resp.EFFR) + " | " + strings.Join(parts, " | ")
}

func (s *USMacroReportSource) Name() string { return s.name }
func (s *USMacroReportSource) Type() string { return "us-macro-report" }

// usMacroReleaseIDs 未来发布日历只看这些关键发布(2026-08-02 新增)。
// ID 为 FRED release_id:10 CPI / 50 非农 / 53 GDP / 54 PCE /
// 192 JOLTS / 27 新屋开工 / 13 工业产出 / 46 PPI。其余 300+ 发布全是噪音。
// 不含 FOMC(101):FRED 该 release 在 include_release_dates_with_no_data=true
// 下返回每天,无法用;FOMC 公告由 fed-press 实时推送。
var usMacroReleaseIDs = map[int]string{
	10:  "CPI",
	50:  "非农",
	53:  "GDP",
	54:  "PCE",
	192: "JOLTS",
	27:  "新屋开工",
	13:  "工业产出",
	46:  "PPI",
}

// fetchNextReleases 未来 N 天官方发布日历(仅保留 usMacroReleaseIDs)。
// FRED 全量日历一次拉(7 天窗口约百条),过滤后排序。
func (s *USMacroReportSource) fetchNextReleases(ctx context.Context, days int) []dsfred.ReleaseDate {
	today := time.Now().Format("2006-01-02")
	end := time.Now().AddDate(0, 0, days).Format("2006-01-02")
	dates, err := dsfred.ReleaseDates(ctx, s.apiKey, today, end)
	if err != nil {
		slog.Warn("fred release dates failed", "source", s.name, "error", err)
		return nil
	}
	out := filterKnownReleases(dates)
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// filterKnownReleases 保留关键发布(纯函数,便于测试)。
func filterKnownReleases(dates []dsfred.ReleaseDate) []dsfred.ReleaseDate {
	out := make([]dsfred.ReleaseDate, 0, len(dates))
	for _, d := range dates {
		if _, ok := usMacroReleaseIDs[d.ReleaseID]; ok {
			out = append(out, d)
		}
	}
	return out
}

// interpretMacroSignals 规则化数据解读(2026-08-02 新增):纯数据看不懂,
// 按关键状态/异动输出大白话信号。仅做确定性强的判断,深度分析交给 AI 段。
func interpretMacroSignals(points []*usMacroPoint) []string {
	var out []string
	val := func(sid string) (float64, bool) {
		for _, p := range points {
			if p.SeriesID == sid {
				return p.Value, true
			}
		}
		return 0, false
	}
	chg := func(sid string) (float64, bool) {
		for _, p := range points {
			if p.SeriesID == sid && p.HasChange {
				return p.ChangePct, true
			}
		}
		return 0, false
	}

	// 1. 收益率曲线倒挂 —— 最强信号
	if t10y2y, ok := val("T10Y2Y"); ok && t10y2y < 0 {
		out = append(out, fmt.Sprintf("⚠️ 收益率曲线倒挂(10Y-2Y 利差 %.2f 个百分点),历史上是衰退预警,市场往往提前定价降息", t10y2y))
	}
	// 2. 长端利率急升(债市抛售)
	if ch, ok := chg("DGS10"); ok && ch > 3 {
		out = append(out, fmt.Sprintf("⚠️ 10 年期国债收益率较前值 +%.1f%%,债市承压,压制成长股估值", ch))
	}
	// 3. 流动性:RRP 逆回购明显收缩 = 回收放缓
	if ch, ok := chg("RRPONTSYD"); ok && ch < -5 {
		out = append(out, fmt.Sprintf("🔻 RRP 逆回购较前值 -%.1f%%,流动性回收放缓,对风险资产偏友好", -ch))
	}
	// 4. 货币供应收缩
	if ch, ok := chg("M2SL"); ok && ch < 0 {
		out = append(out, fmt.Sprintf("🔻 M2 货币供应环比 -%.2f%%,货币条件收紧", -ch))
	}
	// 5. 美元异动
	if ch, ok := chg("DTWEXBGS"); ok && ch > 1 {
		out = append(out, fmt.Sprintf("🌐 美元指数较前值 +%.1f%%,对新兴市场和大宗商品构成压力", ch))
	}
	// 6. 失业率回升
	if ch, ok := chg("UNRATE"); ok && ch > 0 {
		out = append(out, fmt.Sprintf("📉 失业率升至 %.2f%%,就业边际转弱,支持降息预期", valOrZero("UNRATE", points)))
	}
	return out
}

func valOrZero(sid string, points []*usMacroPoint) float64 {
	for _, p := range points {
		if p.SeriesID == sid {
			return p.Value
		}
	}
	return 0
}

type fredObs struct {
	Date  string `json:"date"`
	Value string `json:"value"`
}

type usMacroPoint struct {
	SeriesID  string
	NameCN    string
	Category  string
	Value     float64
	Date      string
	ChangePct float64 // 相对上一观测
	HasChange bool
}

// Fetch 并发拉 FRED 所有指标 + 美股指数快照 + 流动性 + 渲染
func (s *USMacroReportSource) Fetch() ([]*model.Message, error) {
	// 美股也仅周一至周五 (美东周末 + US 节假日会关市, 但 FRED 数据
	// 基本每周都有更新, 简化为排除周末)
	w := time.Now().Weekday()
	if w == time.Saturday || w == time.Sunday {
		return nil, nil
	}
	// 顺序链最坏 ~190s:FRED 全量(~30s 慢日)+ 发布日历(~27s)+ fedwatch 脚本(90s)+ LLM(45s)。
	// 90s 会在 LLM 前到期导致 AI 段静默丢失,放宽到 240s。
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	// 并发拉 FRED
	points := s.fetchFREDAll(ctx)
	// 美股指数
	indices := s.fetchIndices(ctx)
	// 流动性 (量比)
	liquidity := s.fetchLiquidity(ctx)
	// 未来发布日历
	releases := s.fetchNextReleases(ctx, 7)
	// 规则解读(大白话信号,纯数据看不懂)
	signals := interpretMacroSignals(points)
	// FedWatch 利率路径(自建,脚本失败为空)
	fedwatch := s.fetchFedWatch(ctx)

	// AI 联动分析 (可选)
	narrative := s.generateNarrative(ctx, points, indices, liquidity)

	content := s.render(points, indices, liquidity, releases, signals, fedwatch, narrative)
	short := s.renderShort(points, indices, releases, signals, fedwatch, narrative)
	id := fmt.Sprintf("us_macro_%s", time.Now().Format("20060102"))
	msg := &model.Message{
		Type:         model.TypeAnalysis,
		ID:           id,
		Title:        "🌎 美国宏观 · 盘前",
		Content:      content,
		ShortContent: short,
		Source:       s.name,
		SourceType:   "us-macro-report",
		CreateTime:   time.Now(),
		FetchTime:    time.Now(),
		Tags:         []string{"美股", "宏观", "FRED", "报告"},
	}
	msg.SetMetadata("display_source", "FRED + 美股流动性")
	// Discord embed 附十年期国债走势图(FRED 官方图表直链,零自绘)
	msg.ImageURL = "https://fred.stlouisfed.org/graph/fredgraph.png?id=DGS10&range=1y"

	return []*model.Message{msg}, nil
}

func (s *USMacroReportSource) fetchFREDAll(ctx context.Context) []*usMacroPoint {
	out := make([]*usMacroPoint, 0, len(s.series))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, sid := range s.series {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			// 巴菲特指标:两个序列相除,不走普通观测路径
			if id == "BUFFETT_INDEX" {
				if pt := s.fetchBuffettIndex(ctx); pt != nil {
					mu.Lock()
					out = append(out, pt)
					mu.Unlock()
				}
				return
			}
			// 劳动缺口:JOLTS 职位空缺 - 失业人数
			if id == "LABOR_GAP" {
				if pt := s.fetchLaborGap(ctx); pt != nil {
					mu.Lock()
					out = append(out, pt)
					mu.Unlock()
				}
				return
			}
			obs, err := s.fetchFREDObservations(ctx, id, 10)
			if err != nil || len(obs) == 0 {
				return
			}
			pt, ok := newUSMacroPoint(id, obs)
			if !ok {
				return
			}
			mu.Lock()
			out = append(out, pt)
			mu.Unlock()
		}(sid)
	}
	wg.Wait()

	// 按预设顺序
	order := map[string]int{}
	for i, sid := range s.series {
		order[sid] = i
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i].SeriesID] < order[out[j].SeriesID] })
	return out
}

func newUSMacroPoint(seriesID string, obs []fredObs) (*usMacroPoint, bool) {
	var values []fredObs
	for _, o := range obs {
		if o.Value == "." || strings.TrimSpace(o.Value) == "" {
			continue
		}
		if len(values) > 0 && o.Date == values[len(values)-1].Date {
			continue // 同日修订值不应被当作前一期
		}
		if _, err := strconv.ParseFloat(o.Value, 64); err == nil {
			values = append(values, o)
		}
		if len(values) == 2 {
			break
		}
	}
	if len(values) == 0 {
		return nil, false
	}
	value, _ := strconv.ParseFloat(values[0].Value, 64)
	pt := &usMacroPoint{SeriesID: seriesID, Value: value, Date: values[0].Date}
	if meta, ok := usMacroMeta[seriesID]; ok {
		pt.NameCN, pt.Category = meta.NameCN, meta.Category
	} else {
		pt.NameCN, pt.Category = seriesID, "其他"
	}
	if len(values) > 1 {
		previous, _ := strconv.ParseFloat(values[1].Value, 64)
		if previous != 0 {
			pt.ChangePct = (value - previous) / previous * 100
			pt.HasChange = true
		}
	}
	return pt, true
}

// fetchBuffettIndex 巴菲特指标 = 非金融企业股权市值(NCBEILQ027S)÷ 名义 GDP(GDP)。
// 两者均为季度序列,取各自最新有效观测相除。
func (s *USMacroReportSource) fetchBuffettIndex(ctx context.Context) *usMacroPoint {
	equity, err := s.fetchFREDObservations(ctx, "NCBEILQ027S", 3)
	if err != nil {
		slog.Warn("buffett index equity failed", "source", s.name, "error", err)
		return nil
	}
	gdp, err := s.fetchFREDObservations(ctx, "GDP", 3)
	if err != nil {
		slog.Warn("buffett index gdp failed", "source", s.name, "error", err)
		return nil
	}
	return computeBuffett(equity, gdp)
}

// computeBuffett 纯函数:equity/gdp 均为最新在前。
// NCBEILQ027S 单位为百万美元,名义 GDP 为十亿美元 → equity 除 1000 对齐。
func computeBuffett(equity, gdp []fredObs) *usMacroPoint {
	e := latestNumeric(equity) / 1000
	g := latestNumeric(gdp)
	if e <= 0 || g <= 0 {
		return nil
	}
	pt := &usMacroPoint{
		SeriesID: "BUFFETT_INDEX",
		NameCN:   "巴菲特指标(市值/GDP)",
		Category: "市场",
		Value:    e / g * 100,
		Date:     time.Now().Format("2006-01-02"),
	}
	// 环比:跳过最新,取第二个有效值再算一次
	prevE, prevG := 0.0, 0.0
	for i := 1; i < len(equity); i++ {
		if v, err := strconv.ParseFloat(equity[i].Value, 64); err == nil && v > 0 {
			prevE = v
			break
		}
	}
	for i := 1; i < len(gdp); i++ {
		if v, err := strconv.ParseFloat(gdp[i].Value, 64); err == nil && v > 0 {
			prevG = v
			break
		}
	}
	if prevE > 0 && prevG > 0 {
		prev := prevE / 1000 / prevG * 100
		if prev != 0 {
			pt.ChangePct = (pt.Value - prev) / prev * 100
			pt.HasChange = true
		}
	}
	return pt
}

// fetchLaborGap 劳动缺口 = JOLTS 职位空缺(JTSJOL,千人)- 失业人数(UNEMPLOY,千人)。
func (s *USMacroReportSource) fetchLaborGap(ctx context.Context) *usMacroPoint {
	openings, err := s.fetchFREDObservations(ctx, "JTSJOL", 3)
	if err != nil {
		slog.Warn("labor gap openings failed", "source", s.name, "error", err)
		return nil
	}
	unemployed, err := s.fetchFREDObservations(ctx, "UNEMPLOY", 3)
	if err != nil {
		slog.Warn("labor gap unemployed failed", "source", s.name, "error", err)
		return nil
	}
	return computeLaborGap(openings, unemployed)
}

// computeLaborGap 纯函数:缺口 = 最新空缺 - 最新失业(千),环比用各自第二新值。
func computeLaborGap(openings, unemployed []fredObs) *usMacroPoint {
	o := latestNumeric(openings)
	u := latestNumeric(unemployed)
	if o <= 0 || u <= 0 {
		return nil
	}
	pt := &usMacroPoint{
		SeriesID: "LABOR_GAP",
		NameCN:   "劳动缺口(空缺-失业)",
		Category: "就业",
		Value:    o - u,
		Date:     time.Now().Format("2006-01-02"),
	}
	prevO, prevU := 0.0, 0.0
	for i := 1; i < len(openings); i++ {
		if v, err := strconv.ParseFloat(openings[i].Value, 64); err == nil && v > 0 {
			prevO = v
			break
		}
	}
	for i := 1; i < len(unemployed); i++ {
		if v, err := strconv.ParseFloat(unemployed[i].Value, 64); err == nil && v > 0 {
			prevU = v
			break
		}
	}
	if prevO > 0 && prevU > 0 {
		prev := prevO - prevU
		if prev != 0 {
			pt.ChangePct = (pt.Value - prev) / prev * 100
			pt.HasChange = true
		}
	}
	return pt
}

// latestNumeric 取最新一个可解析数值。
func latestNumeric(obs []fredObs) float64 {
	for _, o := range obs {
		if v, err := strconv.ParseFloat(o.Value, 64); err == nil && v > 0 {
			return v
		}
	}
	return 0
}

// fetchFREDObservations pulls the latest `limit` observations newest-first via
// the shared datasources/fred package (keyed api.stlouisfed.org). One fetch
// owner across the Go stack — replaces the old data-gateway subprocess + the
// duplicate direct-HTTP fallback.
func (s *USMacroReportSource) fetchFREDObservations(ctx context.Context, seriesID string, limit int) ([]fredObs, error) {
	obs, err := dsfred.Observations(ctx, s.apiKey, dsfred.Query{
		Series:   seriesID,
		Limit:    limit,
		SortDesc: true,
	})
	if err != nil {
		return nil, err
	}
	res := make([]fredObs, 0, len(obs))
	for _, o := range obs {
		res = append(res, fredObs{Date: o.Date, Value: o.Value})
	}
	return res, nil
}

func (s *USMacroReportSource) fetchIndices(ctx context.Context) map[string]*model.IndexQuote {
	if s.marketService == nil {
		return nil
	}
	m, err := s.marketService.GetUSIndexSnapshot(ctx)
	if err != nil {
		return nil
	}
	return m
}

func (s *USMacroReportSource) fetchLiquidity(ctx context.Context) *market.USMarketLiquidity {
	if s.marketService == nil {
		return nil
	}
	liq, err := s.marketService.GetUSMarketLiquidity(ctx)
	if err != nil {
		return nil
	}
	return liq
}

func (s *USMacroReportSource) render(points []*usMacroPoint, indices map[string]*model.IndexQuote, liq *market.USMarketLiquidity, releases []dsfred.ReleaseDate, signals []string, fedwatch, narrative string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📅 %s\n\n", time.Now().Format("2006-01-02 15:04")))

	// FRED 按分类
	if len(points) > 0 {
		byCat := map[string][]*usMacroPoint{}
		for _, p := range points {
			byCat[p.Category] = append(byCat[p.Category], p)
		}
		catOrder := []string{"利率", "流动性", "利差", "市场", "就业", "通胀", "经济", "商品", "其他"}
		b.WriteString("── FRED 关键指标 ──\n")
		for _, cat := range catOrder {
			pts, ok := byCat[cat]
			if !ok || len(pts) == 0 {
				continue
			}
			b.WriteString(fmt.Sprintf("【%s】", cat))
			for i, p := range pts {
				if i > 0 {
					b.WriteString(" | ")
				}
				b.WriteString(fmt.Sprintf("%s %s", p.NameCN, formatMacroValue(p.SeriesID, p.Value)))
				if p.HasChange {
					b.WriteString(fmt.Sprintf(" (较前值 %+.2f%%)", p.ChangePct))
				}
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// 美股指数
	if len(indices) > 0 {
		b.WriteString("── 美股指数 ──\n")
		order := []struct{ sym, label string }{
			{"^DJI", "DJI"}, {"^GSPC", "SPX"}, {"^IXIC", "NDX"},
			{"^RUT", "RUT"}, {"^VIX", "VIX"},
		}
		parts := []string{}
		for _, o := range order {
			if q, ok := indices[o.sym]; ok {
				parts = append(parts, fmt.Sprintf("%s %+.2f%%", o.label, q.ChangePercent))
			}
		}
		b.WriteString(strings.Join(parts, " | "))
		b.WriteString("\n\n")
	}

	// 流动性
	if liq != nil {
		b.WriteString("── 美股流动性 ──\n")
		b.WriteString(fmt.Sprintf("%s 量比: %.2f (当前 %d / 5日均 %d)\n",
			liq.Symbol, liq.VolumeRatio, liq.Volume, liq.VolumeMA5))
		b.WriteString("\n")
	}

	// 未来 7 天官方发布日历
	if len(releases) > 0 {
		b.WriteString("── 未来 7 天官方发布 ──\n")
		var parts []string
		for _, r := range releases {
			parts = append(parts, fmt.Sprintf("%s %s", r.Date[5:], usMacroReleaseIDs[r.ReleaseID]))
		}
		b.WriteString(strings.Join(parts, " | ") + "\n\n")
	}

	// 数据解读(规则化大白话信号)
	if len(signals) > 0 {
		b.WriteString("── 数据解读 ──\n")
		for _, sg := range signals {
			b.WriteString("- " + sg + "\n")
		}
		b.WriteString("\n")
	}

	// FedWatch 利率路径
	if fedwatch != "" {
		b.WriteString("── 利率路径 (FedWatch) ──\n" + fedwatch + "\n\n")
	}

	// AI 联动分析
	if narrative != "" {
		b.WriteString(FormatLLMSection("🤖 AI 联动分析 (对 A 股启示)", narrative))
	}

	return b.String()
}

// renderShort 精简版 — 给 wechat 用, 核心利率 + 美股三指数 + AI 分析
func (s *USMacroReportSource) renderShort(points []*usMacroPoint, indices map[string]*model.IndexQuote, releases []dsfred.ReleaseDate, signals []string, fedwatch, narrative string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🌎 美国宏观 | %s\n\n", time.Now().Format("01-02")))

	// 核心利率
	core := map[string]bool{"DFF": true, "DGS10": true, "CPIAUCSL": true}
	var rateLines []string
	for _, p := range points {
		if core[p.SeriesID] {
			line := fmt.Sprintf("%s %s", p.NameCN, formatMacroValue(p.SeriesID, p.Value))
			if p.HasChange {
				line += fmt.Sprintf(" (较前值 %+.2f%%)", p.ChangePct)
			}
			rateLines = append(rateLines, line)
		}
	}
	if len(rateLines) > 0 {
		b.WriteString("关键指标: " + strings.Join(rateLines, " | ") + "\n")
	}

	// 三指数
	var idxParts []string
	for _, sym := range []string{"^GSPC", "^IXIC", "^VIX"} {
		if q, ok := indices[sym]; ok {
			name := map[string]string{"^GSPC": "SPX", "^IXIC": "NDX", "^VIX": "VIX"}[sym]
			idxParts = append(idxParts, fmt.Sprintf("%s %+.2f%%", name, q.ChangePercent))
		}
	}
	if len(idxParts) > 0 {
		b.WriteString("美股: " + strings.Join(idxParts, " | ") + "\n")
	}

	// 未来发布日历(精简一行)
	if len(releases) > 0 {
		var relParts []string
		for _, r := range releases {
			relParts = append(relParts, fmt.Sprintf("%s %s", r.Date[5:], usMacroReleaseIDs[r.ReleaseID]))
		}
		b.WriteString("📅 未来发布: " + strings.Join(relParts, " | ") + "\n")
	}

	// 数据解读(精简一行)
	if len(signals) > 0 {
		b.WriteString("💡 解读: " + strings.Join(signals, "；") + "\n")
	}

	// FedWatch 利率路径(精简一行)
	if fedwatch != "" {
		b.WriteString("🎯 " + fedwatch + "\n")
	}

	if narrative != "" {
		b.WriteString("\n🤖 ")
		b.WriteString(narrative)
	}

	return b.String()
}

// formatMacroValue 按 seriesID 格式化数值
func formatMacroValue(seriesID string, val float64) string {
	meta, ok := usMacroMeta[seriesID]
	if !ok {
		return fmt.Sprintf("%.2f", val)
	}
	return fmt.Sprintf("%.*f%s", meta.Decimals, val/meta.Scale, meta.Unit)
}

// generateNarrative AI 联动分析 (对 A 股启示)
func (s *USMacroReportSource) generateNarrative(ctx context.Context, points []*usMacroPoint,
	indices map[string]*model.IndexQuote, liq *market.USMarketLiquidity) string {
	if s.llmProvider == nil {
		return ""
	}

	// 整理 key 指标
	var dataLines []string
	for _, p := range points {
		if !p.HasChange {
			continue // 只看有变化的
		}
		dataLines = append(dataLines, fmt.Sprintf("%s %s (较前值 %+.2f%%)",
			p.NameCN, formatMacroValue(p.SeriesID, p.Value), p.ChangePct))
	}
	// 即便没变化也列出几个核心锚点
	if len(dataLines) == 0 {
		for _, p := range points {
			if p.SeriesID == "DGS10" || p.SeriesID == "DFF" || p.SeriesID == "CPIAUCSL" {
				dataLines = append(dataLines, fmt.Sprintf("%s %s",
					p.NameCN, formatMacroValue(p.SeriesID, p.Value)))
			}
		}
	}

	var idxLines []string
	for _, sym := range []string{"^GSPC", "^IXIC", "^VIX"} {
		if q, ok := indices[sym]; ok {
			name := map[string]string{"^GSPC": "标普", "^IXIC": "纳指", "^VIX": "VIX"}[sym]
			idxLines = append(idxLines, fmt.Sprintf("%s %+.2f%%", name, q.ChangePercent))
		}
	}

	liqLine := ""
	if liq != nil {
		liqLine = fmt.Sprintf("SPY 量比 %.2f", liq.VolumeRatio)
	}

	prompt := fmt.Sprintf(`你是资深宏观分析师。基于今晚美股盘前的数据:

FRED 关键指标: %s
美股指数: %s
流动性: %s

请先用一两句话点出最关键的 1-2 个信号(如收益率曲线倒挂、美元异动、流动性变化、就业转弱)及其含义, 再从美国宏观角度分析对次日 (北京时间) A 股可能的影响, 250 字以内。重点回答:
1) 货币政策 / 利率走向对 A 股资金面的影响
2) 美股情绪传导 (科技/消费/金融板块)
3) 风险点 (如有)

直接输出分析结论, 不要前置寒暄。`,
		strings.Join(dataLines, " | "),
		strings.Join(idxLines, " | "),
		liqLine,
	)

	return LLMSummarizeWithSource(ctx, s.llmProvider, s.name, prompt, "thinking", 45*time.Second)
}
