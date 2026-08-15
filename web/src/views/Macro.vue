<template>
  <div class="macro-root">
    <div class="macro-header">
      <h2>🇺🇸 美股宏观</h2>
      <span class="updated">数据源 FRED · 更新 {{ updated }}</span>
    </div>

    <!-- 未来发布日历 -->
    <el-card class="release-card" shadow="never">
      <template #header>
        <div class="card-title">📅 未来 7 天官方发布</div>
      </template>
      <div v-if="releases.length" class="release-chips">
        <el-tag
          v-for="r in releases"
          :key="r.date + r.name"
          size="large"
          effect="plain"
        >{{ r.date.slice(5) }} · {{ r.name }}</el-tag>
      </div>
      <div v-else class="empty-hint">本周无关键发布(非农/GDP/CPI/FOMC 等)</div>
    </el-card>

    <!-- 关键指标概览表 -->
    <el-card shadow="never" class="table-card">
      <template #header>
        <div class="card-title">关键指标</div>
      </template>
      <el-table :data="tableRows" size="small" stripe>
        <el-table-column prop="category" label="分类" width="76" />
        <el-table-column prop="name" label="指标" min-width="130" />
        <el-table-column prop="valueText" label="最新值" min-width="110" align="right" />
        <el-table-column prop="date" label="日期" width="92" align="right" />
        <el-table-column prop="changeText" label="环比" width="96" align="right">
          <template #default="{ row }">
            <span :class="row.changeClass">{{ row.changeText }}</span>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 走势图 -->
    <div class="chart-toolbar">
      <span class="card-title">走势图</span>
      <el-radio-group v-model="range" size="small" @change="loadCharts">
        <el-radio-button value="6m">6月</el-radio-button>
        <el-radio-button value="1y">1年</el-radio-button>
        <el-radio-button value="3y">3年</el-radio-button>
        <el-radio-button value="5y">5年</el-radio-button>
      </el-radio-group>
    </div>
    <div class="chart-grid">
      <el-card v-for="ch in charts" :key="ch.series" shadow="never" class="chart-card">
        <template #header>
          <div class="card-title">
            {{ ch.name }}
            <span class="chart-sub">{{ ch.unit }}</span>
          </div>
        </template>
        <div :ref="el => setChartEl(ch.series, el)" class="chart-box" />
      </el-card>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { getMacroOverview, getMacroSeries } from '../api/macro'
import * as echarts from 'echarts'

// 图表序列:series id → 中文名(单序列折线,一图一轴)
const CHARTS = [
  { series: 'DGS10', name: '10 年期国债收益率', unit: '%' },
  { series: 'DGS30', name: '30 年期国债收益率', unit: '%' },
  { series: 'T10Y2Y', name: '10Y-2Y 利差', unit: '%', diverging: true },
  { series: 'WALCL', name: 'Fed 总资产', unit: '万亿美元' },
  { series: 'CPIAUCSL', name: 'CPI', unit: '指数' },
  { series: 'UNRATE', name: '失业率', unit: '%' },
]

const updated = ref('')
const releases = ref([])
const tableRows = ref([])
const range = ref('1y')
const charts = ref(CHARTS)

const chartEls = {}
const chartInstances = {}

function setChartEl(series, el) {
  if (el) chartEls[series] = el
}

// FRED 原始值 → 展示值的单位换算(与后端 meta.Scale 对齐)
const SCALE = {
  M2SL: 1e3, RRPONTSYD: 1e3, PAYEMS: 1e3, DGORDER: 1e3, BOPGSTB: 1e3, // 百万→十万亿/百万人/十亿美元
  WALCL: 1e6, TOTRESNS: 1e6, RSXFS: 1e6, // 百万美元→万亿美元
  ICSA: 1e4, // 人→万人
  LABOR_GAP: 10, // 千人→万人
}
const DECIMALS = { RRPONTSYD: 3, ICSA: 1, LABOR_GAP: 1, DGORDER: 1, BOPGSTB: 1 }

function fmtVal(row) {
  let v = row.value
  if (SCALE[row.series_id]) v = v / SCALE[row.series_id]
  return v.toFixed(DECIMALS[row.series_id] ?? 2)
}

async function loadOverview() {
  const { data: d } = await getMacroOverview()
  updated.value = new Date(d.updated).toLocaleString('zh-CN', { hour12: false })
  releases.value = d.releases || []
  tableRows.value = (d.indicators || []).map(r => {
    const change = r.has_prev && r.prev !== 0 ? ((r.value - r.prev) / r.prev) * 100 : null
    return {
      ...r,
      name: r.name_cn,
      valueText: `${fmtVal(r)} ${r.unit}`,
      changeText: change === null ? '—' : `${change >= 0 ? '+' : ''}${change.toFixed(2)}%`,
      changeClass: change === null ? 'change-none' : change >= 0 ? 'change-up' : 'change-down',
    }
  })
}

async function loadCharts() {
  for (const ch of CHARTS) {
    const { data: d } = await getMacroSeries(ch.series, range.value)
    renderChart(ch, d.points || [])
  }
}

function renderChart(ch, points) {
  const el = chartEls[ch.series]
  if (!el) return
  const inst = chartInstances[ch.series] || echarts.init(el)
  chartInstances[ch.series] = inst

  const data = points.map(p => [p.date, p.value])
  const series = {
    type: 'line',
    data,
    showSymbol: false,
    smooth: false,
    lineStyle: { width: 2, color: '#409eff' },
    areaStyle: {
      color: {
        type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
        colorStops: [
          { offset: 0, color: 'rgba(64,158,255,0.18)' },
          { offset: 1, color: 'rgba(64,158,255,0)' },
        ],
      },
    },
  }
  // 利差图:极性配色(绿正/红负),二次编码:数值永远同显
  if (ch.diverging) {
    series.lineStyle = { width: 2, color: '#67c23a' }
    series.itemStyle = { color: params => (params.value[1] >= 0 ? '#67c23a' : '#f56c6c') }
  }
  const markLine = ch.diverging
    ? { markLine: {
        symbol: 'none',
        lineStyle: { color: '#909399', width: 1, type: 'dashed' },
        data: [{ yAxis: 0 }],
      } }
    : {}

  inst.setOption({
    grid: { left: 8, right: 8, top: 12, bottom: 8, containLabel: true },
    xAxis: {
      type: 'time',
      axisLine: { lineStyle: { color: '#dcdfe6' } },
      axisLabel: { color: '#909399', fontSize: 11 },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      scale: true,
      axisLabel: { color: '#909399', fontSize: 11 },
      splitLine: { lineStyle: { color: '#ebeef5', width: 1 } },
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' },
      valueFormatter: v => (v === null || v === undefined ? '—' : Number(v).toFixed(2)),
    },
    series: { ...series, ...markLine },
  })
}

function resizeAll() {
  Object.values(chartInstances).forEach(i => i && i.resize())
}

onMounted(async () => {
  await loadOverview()
  await loadCharts()
  window.addEventListener('resize', resizeAll)
})
onBeforeUnmount(() => {
  window.removeEventListener('resize', resizeAll)
  Object.values(chartInstances).forEach(i => i && i.dispose())
  chartInstances.length = 0
})
</script>

<style scoped>
.macro-root {
  padding: 4px;
  color-scheme: light;
  --text-secondary: #52514e;
}
.macro-header {
  display: flex;
  align-items: baseline;
  gap: 12px;
  margin-bottom: 12px;
}
.macro-header h2 { margin: 0; font-size: 18px; }
.updated { color: var(--text-secondary); font-size: 12px; }
.card-title { font-size: 14px; font-weight: 600; display: flex; align-items: baseline; gap: 8px; }
.chart-sub { color: var(--text-secondary); font-size: 12px; font-weight: 400; }
.release-card { margin-bottom: 12px; }
.release-chips { display: flex; flex-wrap: wrap; gap: 8px; }
.empty-hint { color: var(--text-secondary); font-size: 13px; }
.table-card { margin-bottom: 16px; }
.chart-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.chart-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
  gap: 12px;
}
.chart-card :deep(.el-card__body) { padding: 8px; }
.chart-box { height: 220px; }
.change-up { color: #f56c6c; }
.change-down { color: #67c23a; }
.change-none { color: var(--text-secondary); }

@media (prefers-color-scheme: dark) {
  .macro-root { color-scheme: dark; --text-secondary: #c3c2b7; }
}
</style>
