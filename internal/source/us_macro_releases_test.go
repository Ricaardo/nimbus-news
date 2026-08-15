package source

import (
	"context"
	"os"
	"strings"
	"testing"

	dsfred "github.com/Ricaardo/nimbus-os/datasources/fred"
)

// TestFetchNextReleasesLive 走真实 FRED release/dates 接口,验证未来发布
// 日历链路(与 TestFetchFREDObservations 同模式:无 key/网络不可达则跳过)。
func TestFetchNextReleasesLive(t *testing.T) {
	key := os.Getenv("FRED_API_KEY")
	if key == "" {
		t.Skip("FRED_API_KEY unset")
	}
	s := &USMacroReportSource{apiKey: key}
	dates := s.fetchNextReleases(context.Background(), 14)
	if len(dates) == 0 {
		t.Skipf("no key releases in window (weekend/数据空窗)")
	}
	for _, d := range dates {
		if _, ok := usMacroReleaseIDs[d.ReleaseID]; !ok {
			t.Fatalf("filtered kept unknown release %d", d.ReleaseID)
		}
		if d.Date == "" {
			t.Fatalf("release %d missing date", d.ReleaseID)
		}
	}
}

// TestFilterKnownReleases 只保留关键发布 ID, 忽略其余 300+ 发布噪音。
func TestFilterKnownReleases(t *testing.T) {
	dates := []dsfred.ReleaseDate{
		{ReleaseID: 50, ReleaseName: "Employment Situation", Date: "2026-08-07"},
		{ReleaseID: 10, ReleaseName: "Consumer Price Index", Date: "2026-08-12"},
		{ReleaseID: 999, ReleaseName: "Some Obscure Series", Date: "2026-08-05"},
		{ReleaseID: 101, ReleaseName: "FOMC Press Release (daily junk, must be dropped)", Date: "2026-08-15"},
	}
	got := filterKnownReleases(dates)
	if len(got) != 2 {
		t.Fatalf("filtered=%d, want 2 (obscure + FOMC-daily must be dropped): %+v", len(got), got)
	}
	for _, d := range got {
		if _, ok := usMacroReleaseIDs[d.ReleaseID]; !ok {
			t.Fatalf("filtered kept unknown release %d", d.ReleaseID)
		}
	}
}

// TestUSMacroRenderReleaseCalendar render 的未来发布段格式。
func TestUSMacroRenderReleaseCalendar(t *testing.T) {
	s := &USMacroReportSource{}
	releases := []dsfred.ReleaseDate{
		{ReleaseID: 50, Date: "2026-08-07"},
		{ReleaseID: 27, Date: "2026-08-18"},
	}
	content := s.render(nil, nil, nil, releases, nil, "", "")
	if !strings.Contains(content, "── 未来 7 天官方发布 ──") {
		t.Fatalf("missing calendar section:\n%s", content)
	}
	if !strings.Contains(content, "08-07 非农") || !strings.Contains(content, "08-18 新屋开工") {
		t.Fatalf("calendar rendering wrong:\n%s", content)
	}
	if !strings.Contains(content, "08-07 非农 | 08-18 新屋开工") {
		t.Fatalf("calendar order/format wrong:\n%s", content)
	}

	// 空日历不渲染段
	empty := s.render(nil, nil, nil, nil, nil, "", "")
	if strings.Contains(empty, "未来 7 天") {
		t.Fatalf("empty calendar must not render section:\n%s", empty)
	}
}

// TestUSMacroRenderShortReleaseCalendar 精简版一行日历。
func TestUSMacroRenderShortReleaseCalendar(t *testing.T) {
	s := &USMacroReportSource{}
	releases := []dsfred.ReleaseDate{
		{ReleaseID: 50, Date: "2026-08-07"},
	}
	short := s.renderShort(nil, nil, releases, nil, "", "")
	if !strings.Contains(short, "📅 未来发布: 08-07 非农") {
		t.Fatalf("short calendar line wrong:\n%s", short)
	}
}

// TestComputeBuffettIndex 巴菲特指标 = 股权市值/名义 GDP×100,最新在前。
// 股权单位百万美元(除 1000 对齐十亿美元)。
func TestComputeBuffettIndex(t *testing.T) {
	equity := []fredObs{
		{Date: "2026-06-01", Value: "69511628.0"}, // 最新: $69.5T
		{Date: "2026-03-01", Value: "71863086.0"}, // 前一期: $71.9T
		{Date: "2025-12-01", Value: "."},
	}
	gdp := []fredObs{
		{Date: "2026-06-01", Value: "32475.21"},
		{Date: "2026-03-01", Value: "31865.721"},
		{Date: "2025-12-01", Value: "."},
	}
	pt := computeBuffett(equity, gdp)
	if pt == nil {
		t.Fatal("expected point")
	}
	// 69511628/1000/32475.21×100 ≈ 214.0
	if pt.Value < 213.5 || pt.Value > 214.5 {
		t.Fatalf("value=%.2f, want ~214", pt.Value)
	}
	if !pt.HasChange {
		t.Fatal("expected change")
	}
	// 214.0 vs 71863086/1000/31865.721×100≈225.5 → 约 -5.1%
	if pt.ChangePct > -4.5 || pt.ChangePct < -5.7 {
		t.Fatalf("change=%.2f%%, want ~-5.1%%", pt.ChangePct)
	}

	// 缺数据 → nil
	if computeBuffett([]fredObs{{Date: "2026-01-01", Value: "."}}, gdp) != nil {
		t.Fatal("missing equity must yield nil")
	}
	if computeBuffett(equity, []fredObs{{Date: "2026-01-01", Value: "0"}}) != nil {
		t.Fatal("zero gdp must yield nil")
	}
}

// TestComputeLaborGap 劳动缺口 = 空缺 - 失业(千),环比用第二新值。
func TestComputeLaborGap(t *testing.T) {
	openings := []fredObs{
		{Date: "2026-05-01", Value: "7594"}, // 最新: 759.4 万
		{Date: "2026-04-01", Value: "7800"},
		{Date: "2026-03-01", Value: "."},
	}
	unemployed := []fredObs{
		{Date: "2026-05-01", Value: "7094"},
		{Date: "2026-04-01", Value: "7100"},
		{Date: "2026-03-01", Value: "."},
	}
	pt := computeLaborGap(openings, unemployed)
	if pt == nil {
		t.Fatal("expected point")
	}
	if pt.Value != 500 { // 7594-7094 千
		t.Fatalf("gap=%.0f, want 500", pt.Value)
	}
	if !pt.HasChange {
		t.Fatal("expected change")
	}
	// 500 vs 7800-7100=700 → -28.6%
	if pt.ChangePct < -30 || pt.ChangePct > -27 {
		t.Fatalf("change=%.2f%%, want ~-28.6%%", pt.ChangePct)
	}
	if computeLaborGap([]fredObs{{Date: "2026-01-01", Value: "."}}, unemployed) != nil {
		t.Fatal("missing openings must yield nil")
	}
}

// TestRenderFedWatchSection FedWatch 段在完整版/精简版的渲染。
func TestRenderFedWatchSection(t *testing.T) {
	s := &USMacroReportSource{}
	fedwatch := "EFFR 3.63% | 09-16 加息70% | 10-28 按兵不动 | 12-09 加息50%"
	full := s.render(nil, nil, nil, nil, nil, fedwatch, "")
	if !strings.Contains(full, "── 利率路径 (FedWatch) ──") || !strings.Contains(full, "09-16 加息70%") {
		t.Fatalf("full render missing fedwatch section:\n%s", full)
	}
	short := s.renderShort(nil, nil, nil, nil, fedwatch, "")
	if !strings.Contains(short, "🎯 EFFR 3.63%") {
		t.Fatalf("short render missing fedwatch line:\n%s", short)
	}
	// 空 fedwatch 不渲染段
	if strings.Contains(s.render(nil, nil, nil, nil, nil, "", ""), "FedWatch") {
		t.Fatal("empty fedwatch must not render section")
	}
}

// TestRenderFedWatchBoundedLine 渲染的会议数不超过 4。
func TestRenderFedWatchBoundedLine(t *testing.T) {
	r := fedWatchResp{EFFR: 3.63}
	for i := 0; i < 6; i++ {
		r.Meetings = append(r.Meetings, struct {
			Date    string  `json:"date"`
			Implied float64 `json:"implied"`
			DeltaBP float64 `json:"delta_bp"`
			Probs   struct {
				Cut  float64 `json:"cut"`
				Hold float64 `json:"hold"`
				Hike float64 `json:"hike"`
			} `json:"probs"`
		}{Date: "2026-09-16", Probs: struct {
			Cut  float64 `json:"cut"`
			Hold float64 `json:"hold"`
			Hike float64 `json:"hike"`
		}{Cut: 70}})
	}
	line := renderFedWatch(r)
	if strings.Count(line, "|") != 4 {
		t.Fatalf("fedwatch line meetings=%d, want exactly 4: %s", strings.Count(line, "|"), line)
	}
	if !strings.Contains(line, "EFFR 3.63%") || !strings.Contains(line, "09-16 降息70%") {
		t.Fatalf("fedwatch line wrong: %s", line)
	}
}

// TestRenderFedWatchThresholds 概率阈值:≥50 定方向,否则按兵不动。
func TestRenderFedWatchThresholds(t *testing.T) {
	mk := func(cut, hold, hike float64) fedWatchResp {
		r := fedWatchResp{EFFR: 3.0}
		r.Meetings = append(r.Meetings, struct {
			Date    string  `json:"date"`
			Implied float64 `json:"implied"`
			DeltaBP float64 `json:"delta_bp"`
			Probs   struct {
				Cut  float64 `json:"cut"`
				Hold float64 `json:"hold"`
				Hike float64 `json:"hike"`
			} `json:"probs"`
		}{Date: "2026-09-16", Probs: struct {
			Cut  float64 `json:"cut"`
			Hold float64 `json:"hold"`
			Hike float64 `json:"hike"`
		}{Cut: cut, Hold: hold, Hike: hike}})
		return r
	}
	if !strings.Contains(renderFedWatch(mk(70, 30, 0)), "降息70%") {
		t.Fatal("cut>=50 must render 降息")
	}
	if !strings.Contains(renderFedWatch(mk(0, 30, 70)), "加息70%") {
		t.Fatal("hike>=50 must render 加息")
	}
	if !strings.Contains(renderFedWatch(mk(0, 100, 0)), "按兵不动") {
		t.Fatal("neither>=50 must render 按兵不动")
	}
}

// TestInterpretMacroSignals 规则解读:倒挂/长端急升/RRP 收缩/美元异动/失业回升。
func TestInterpretMacroSignals(t *testing.T) {
	points := []*usMacroPoint{
		{SeriesID: "T10Y2Y", Value: -0.25, HasChange: true, ChangePct: -5},
		{SeriesID: "DGS10", Value: 4.8, HasChange: true, ChangePct: 4.2},
		{SeriesID: "RRPONTSYD", Value: 0.3, HasChange: true, ChangePct: -8.0},
		{SeriesID: "M2SL", Value: 21.4, HasChange: true, ChangePct: 0.3},
		{SeriesID: "DTWEXBGS", Value: 128.0, HasChange: true, ChangePct: 1.5},
		{SeriesID: "UNRATE", Value: 4.3, HasChange: true, ChangePct: 1.2},
	}
	got := interpretMacroSignals(points)
	wantSignals := []string{"倒挂", "债市承压", "流动性回收放缓", "美元指数较前值", "失业率升至 4.30%"}
	if len(got) != 5 {
		t.Fatalf("signals=%d, want 5 (M2 环比为正不触发): %v", len(got), got)
	}
	for i, w := range wantSignals {
		if !strings.Contains(got[i], w) {
			t.Fatalf("signal[%d] missing %q: %v", i, w, got)
		}
	}

	// 无信号时不输出
	if got := interpretMacroSignals([]*usMacroPoint{{SeriesID: "DGS10", Value: 4.0}}); len(got) != 0 {
		t.Fatalf("no-signal case produced: %v", got)
	}
}

// TestUSMacroRenderSignalsSection 解读段在完整版/精简版的渲染。
func TestUSMacroRenderSignalsSection(t *testing.T) {
	s := &USMacroReportSource{}
	signals := []string{"⚠️ 收益率曲线倒挂", "🔻 RRP 逆回购收缩"}
	full := s.render(nil, nil, nil, nil, signals, "", "")
	if !strings.Contains(full, "── 数据解读 ──") || !strings.Contains(full, "收益率曲线倒挂") {
		t.Fatalf("full render missing signals section:\n%s", full)
	}
	short := s.renderShort(nil, nil, nil, signals, "", "")
	if !strings.Contains(short, "💡 解读: ⚠️ 收益率曲线倒挂；🔻 RRP 逆回购收缩") {
		t.Fatalf("short render missing signals line:\n%s", short)
	}
	// 无信号不渲染段
	if strings.Contains(s.render(nil, nil, nil, nil, nil, "", ""), "数据解读") {
		t.Fatal("empty signals must not render section")
	}
}
