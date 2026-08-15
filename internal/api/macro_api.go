package api

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	dsfred "github.com/Ricaardo/nimbus-os/datasources/fred"
	"github.com/gin-gonic/gin"
)

// 美股宏观模块 (2026-08-02 新增, moomoo 宏观模块风格)。
// GET /api/macro/overview — 关键指标最新值/前值/环比 + 未来发布日历
// GET /api/macro/series?series=DGS10&range=1y — 单序列时间序列(图表用)

// macroSeriesMeta 前端展示用(与 source 包 usMacroMeta 同源,数值格式由前端处理)。
type macroSeriesMeta struct {
	NameCN   string
	Category string
	Unit     string
	Decimals int
	Scale    float64
}

var macroSeriesMetaMap = map[string]macroSeriesMeta{
	"DFF":          {"联邦基金利率", "利率", "%", 2, 1},
	"SOFR":         {"SOFR", "利率", "%", 2, 1},
	"DGS2":         {"2年期国债", "利率", "%", 2, 1},
	"DGS10":        {"10年期国债", "利率", "%", 2, 1},
	"DGS30":        {"30年期国债", "利率", "%", 2, 1},
	"MORTGAGE30US": {"30年房贷利率", "利率", "%", 2, 1},
	"M2SL":         {"M2 货币供应", "流动性", "万亿美元", 2, 1e3},
	"WALCL":        {"Fed 总资产", "流动性", "万亿美元", 2, 1e6},
	"RRPONTSYD":    {"RRP 逆回购", "流动性", "万亿美元", 3, 1e3},
	"TOTRESNS":     {"银行储备金", "流动性", "万亿美元", 2, 1e6},
	"T10Y2Y":       {"10Y-2Y 利差", "利差", "个百分点", 2, 1},
	"T10Y3M":       {"10Y-3M 利差", "利差", "个百分点", 2, 1},
	"UNRATE":       {"失业率", "就业", "%", 2, 1},
	"PAYEMS":       {"非农就业", "就业", "百万人", 1, 1e3},
	"CPIAUCSL":     {"CPI", "通胀", "指数", 1, 1},
	"PCEPI":        {"PCE", "通胀", "指数", 1, 1},
	"GDPC1":        {"实际 GDP", "经济", "万亿美元", 2, 1e3},
	"DTWEXBGS":     {"美元指数（广义）", "汇率", "指数", 2, 1},
	"VIXCLS":       {"VIX 恐慌指数", "市场", "指数", 2, 1},
	"BAA10Y":       {"Baa-10Y 信用利差", "利差", "个百分点", 2, 1},
	"T10YIE":       {"10Y 通胀预期", "通胀", "%", 2, 1},
	"DCOILWTICO":   {"WTI 原油", "商品", "美元/桶", 2, 1},
	"BUFFETT_INDEX": {"巴菲特指标(市值/GDP)", "市场", "%", 1, 1},
	"CPILFESL":      {"核心 CPI", "通胀", "指数", 1, 1},
	"PCEPILFE":      {"核心 PCE", "通胀", "指数", 1, 1},
	"RSXFS":         {"零售销售(实际)", "经济", "万亿美元", 2, 1e6},
	"ICSA":          {"初请失业金", "就业", "万人", 1, 1e4},
	"LABOR_GAP":     {"劳动缺口(空缺-失业)", "就业", "万人", 1, 10},
	"PPIFIS":        {"PPI 最终需求", "通胀", "指数", 1, 1},
	"DGORDER":       {"耐用品新订单", "经济", "十亿美元", 1, 1e3},
	"BOPGSTB":       {"贸易余额(净出口)", "经济", "十亿美元", 1, 1e3},
}

// macroOverviewSeries 概览表展示的核心序列(顺序即展示顺序)。
var macroOverviewSeries = []string{
	"DFF", "SOFR", "DGS2", "DGS10", "DGS30", "T10Y2Y", "T10Y3M",
	"UNRATE", "PAYEMS", "ICSA", "LABOR_GAP",
	"CPIAUCSL", "CPILFESL", "PCEPI", "PCEPILFE", "PPIFIS",
	"RSXFS", "DGORDER", "BOPGSTB",
	"DTWEXBGS", "VIXCLS", "BAA10Y", "T10YIE", "DCOILWTICO", "BUFFETT_INDEX",
	"M2SL", "WALCL", "RRPONTSYD",
}

// macroReleaseIDs 未来发布日历白名单(与 source 包 usMacroReleaseIDs 同源)。
// 不含 FOMC(101):FRED 该 release 返回每天,无法用;FOMC 由 fed-press 实时推送。
var macroReleaseIDs = map[int]string{
	10:  "CPI",
	50:  "非农",
	53:  "GDP",
	54:  "PCE",
	192: "JOLTS",
	27:  "新屋开工",
	13:  "工业产出",
	46:  "PPI",
}

type macroOverviewResp struct {
	Updated  time.Time           `json:"updated"`
	Indicators []macroIndicator  `json:"indicators"`
	Releases []macroRelease      `json:"releases"`
}

type macroIndicator struct {
	SeriesID string  `json:"series_id"`
	NameCN   string  `json:"name_cn"`
	Category string  `json:"category"`
	Unit     string  `json:"unit"`
	Value    float64 `json:"value"`
	Date     string  `json:"date"`
	Prev     float64 `json:"prev"`
	ChangePct float64 `json:"change_pct"`
	HasPrev  bool    `json:"has_prev"`
}

type macroRelease struct {
	Date string `json:"date"`
	Name string `json:"name"`
}

// getMacroOverview 关键指标概览表(最新值/前值/环比)+ 未来 7 天发布日历。
func (s *Server) getMacroOverview(c *gin.Context) {
	key := os.Getenv("FRED_API_KEY")
	if key == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "FRED_API_KEY not set"})
		return
	}
	ctx := c.Request.Context()

	resp := macroOverviewResp{Updated: time.Now()}
	// 并发拉取(顺序 20×2s≈40s,并行 ~3s)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, sid := range macroOverviewSeries {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			var obs []dsfred.Observation
			var err error
			// 巴菲特指标:企业股权市值(NCBEILQ027S,百万美元)/1000 ÷ 名义 GDP(十亿美元)
			if sid == "BUFFETT_INDEX" {
				eq, e1 := dsfred.Observations(ctx, key, dsfred.Query{Series: "NCBEILQ027S", Limit: 2, SortDesc: true})
				gdp, e2 := dsfred.Observations(ctx, key, dsfred.Query{Series: "GDP", Limit: 2, SortDesc: true})
				if e1 == nil && e2 == nil && len(eq) > 0 && len(gdp) > 0 {
					eVal, eOK := parseFloatValue(eq[0].Value)
					gVal, gOK := parseFloatValue(gdp[0].Value)
					if eOK == nil && gOK == nil && eVal > 0 && gVal > 0 {
						obs = []dsfred.Observation{{Date: eq[0].Date, Value: fmt.Sprintf("%.4f", eVal/1000/gVal*100)}}
						// 前一期合成观测,供环比计算
						if len(eq) > 1 && len(gdp) > 1 {
							pE, pEok := parseFloatValue(eq[1].Value)
							pG, pGok := parseFloatValue(gdp[1].Value)
							if pEok == nil && pGok == nil && pE > 0 && pG > 0 {
								obs = append(obs, dsfred.Observation{Date: eq[1].Date, Value: fmt.Sprintf("%.4f", pE/1000/pG*100)})
							}
						}
					}
				}
			} else if sid == "LABOR_GAP" {
				// 劳动缺口:JOLTS 空缺(JTSJOL,千人)- 失业人数(UNEMPLOY,千人)
				jo, e1 := dsfred.Observations(ctx, key, dsfred.Query{Series: "JTSJOL", Limit: 2, SortDesc: true})
				un, e2 := dsfred.Observations(ctx, key, dsfred.Query{Series: "UNEMPLOY", Limit: 2, SortDesc: true})
				if e1 == nil && e2 == nil && len(jo) > 0 && len(un) > 0 {
					jVal, jOK := parseFloatValue(jo[0].Value)
					uVal, uOK := parseFloatValue(un[0].Value)
					if jOK == nil && uOK == nil && jVal > 0 && uVal > 0 {
						obs = []dsfred.Observation{{Date: jo[0].Date, Value: fmt.Sprintf("%.1f", jVal-uVal)}}
						if len(jo) > 1 && len(un) > 1 {
							pJ, pJok := parseFloatValue(jo[1].Value)
							pU, pUok := parseFloatValue(un[1].Value)
							if pJok == nil && pUok == nil && pJ > 0 && pU > 0 {
								obs = append(obs, dsfred.Observation{Date: jo[1].Date, Value: fmt.Sprintf("%.1f", pJ-pU)})
							}
						}
					}
				}
			} else {
				obs, err = dsfred.Observations(ctx, key, dsfred.Query{Series: sid, Limit: 2, SortDesc: true})
			}
			if err != nil || len(obs) == 0 {
				return
			}
			ind := macroIndicator{SeriesID: sid}
			if meta, ok := macroSeriesMetaMap[sid]; ok {
				ind.NameCN, ind.Category, ind.Unit = meta.NameCN, meta.Category, meta.Unit
			} else {
				ind.NameCN, ind.Category, ind.Unit = sid, "其他", ""
			}
			if v, err := parseFloatValue(obs[0].Value); err == nil {
				ind.Value = v
				ind.Date = obs[0].Date
			}
			if len(obs) > 1 {
				if prev, err := parseFloatValue(obs[1].Value); err == nil {
					ind.Prev = prev
					ind.HasPrev = true
					if prev != 0 {
						ind.ChangePct = (ind.Value - prev) / prev * 100
					}
				}
			}
			mu.Lock()
			resp.Indicators = append(resp.Indicators, ind)
			mu.Unlock()
		}(sid)
	}
	wg.Wait()
	// 保持配置顺序
	order := make(map[string]int, len(macroOverviewSeries))
	for i, sid := range macroOverviewSeries {
		order[sid] = i
	}
	sort.Slice(resp.Indicators, func(i, j int) bool {
		return order[resp.Indicators[i].SeriesID] < order[resp.Indicators[j].SeriesID]
	})

	// 未来 7 天发布日历
	today := time.Now().Format("2006-01-02")
	end := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	if dates, err := dsfred.ReleaseDates(ctx, key, today, end); err == nil {
		for _, d := range dates {
			if name, ok := macroReleaseIDs[d.ReleaseID]; ok {
				resp.Releases = append(resp.Releases, macroRelease{Date: d.Date, Name: name})
			}
		}
		sort.Slice(resp.Releases, func(i, j int) bool { return resp.Releases[i].Date < resp.Releases[j].Date })
	}

	c.JSON(http.StatusOK, resp)
}

// getMacroSeries 单序列时间序列(range: 6m/1y/3y/5y)。
func (s *Server) getMacroSeries(c *gin.Context) {
	key := os.Getenv("FRED_API_KEY")
	if key == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "FRED_API_KEY not set"})
		return
	}
	sid := c.Query("series")
	if sid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "series required"})
		return
	}
	limit := map[string]int{"6m": 130, "1y": 260, "3y": 800, "5y": 1300}[c.DefaultQuery("range", "1y")]
	if limit == 0 {
		limit = 260
	}
	// SortDesc 取最新 limit 条,再反转为时间正序(FRED 自然序=最早在前,直接 limit 会取到 1976 年)
	obs, err := dsfred.Observations(c.Request.Context(), key, dsfred.Query{Series: sid, Limit: limit, SortDesc: true})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	pts := observationsToPoints(obs, true)
	meta := macroSeriesMetaMap[sid]

	c.JSON(http.StatusOK, gin.H{
		"series_id": sid,
		"name_cn":   meta.NameCN,
		"category":  meta.Category,
		"unit":      meta.Unit,
		"points":    pts,
	})
}

// parseFloatValue 解析 FRED 观测值("." 表示缺失)。
func parseFloatValue(v string) (float64, error) {
	if v == "." || v == "" {
		return 0, fmt.Errorf("missing value")
	}
	return strconv.ParseFloat(v, 64)
}

// point 单个时间序列点。
type point struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

// observationsToPoints 观测序列 → 时间点数组;descToAsc 为 true 时把
// 最新在前的序列反转为时间正序。
func observationsToPoints(obs []dsfred.Observation, descToAsc bool) []point {
	pts := make([]point, 0, len(obs))
	for _, o := range obs {
		if v, err := parseFloatValue(o.Value); err == nil {
			pts = append(pts, point{Date: o.Date, Value: v})
		}
	}
	if descToAsc {
		for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
			pts[i], pts[j] = pts[j], pts[i]
		}
	}
	return pts
}
