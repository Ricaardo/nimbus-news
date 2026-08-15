<template>
  <div class="reports-page">
    <header class="reports-header">
      <h2>Reports</h2>
      <div class="reports-filter">
        <select v-model="selectedType" @change="refresh">
          <option value="">All</option>
          <option value="us-macro-report">us-macro-report</option>
          <option value="guanfu-score">guanfu-score</option>
          <option value="pre-market-briefing">pre-market-briefing</option>
          <option value="closing-briefing">closing-briefing</option>
          <option value="us-preview">us-preview</option>
          <option value="news-aggregate-morning">news-aggregate-morning</option>
          <option value="news-aggregate-evening">news-aggregate-evening</option>
        </select>
        <select v-model="selectedLimit" @change="refresh">
          <option :value="10">10</option>
          <option :value="20">20</option>
          <option :value="50">50</option>
          <option :value="100">100</option>
        </select>
        <button @click="refresh" :disabled="loading">{{ loading ? '...' : 'refresh' }}</button>
      </div>
    </header>

    <div class="reports-main">
      <aside class="reports-list">
        <div v-if="reports.length === 0 && !loading" class="empty">no reports</div>
        <div
          v-for="r in reports"
          :key="r.id"
          class="reports-item"
          :class="{ active: activeId === r.id }"
          @click="select(r)"
        >
          <div class="reports-item-time">{{ fmtTime(r.create_time) }}</div>
          <div class="reports-item-source">{{ r.source }}</div>
          <div class="reports-item-title">{{ r.title }}</div>
        </div>
      </aside>

      <main class="reports-detail">
        <div v-if="!active" class="empty">select a report</div>
        <article v-else>
          <h3>{{ active.title }}</h3>
          <div class="reports-meta">
            <span>{{ active.source }}</span>
            <span>·</span>
            <span>{{ fmtTime(active.create_time) }}</span>
            <span v-if="active.tags && active.tags.length">
              · {{ active.tags.join(' · ') }}
            </span>
          </div>
          <pre class="reports-content">{{ active.content }}</pre>
        </article>
      </main>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { getReports } from '../api/config'

const reports = ref([])
const selectedType = ref('')
const selectedLimit = ref(20)
const loading = ref(false)
const activeId = ref(null)
const active = ref(null)

async function refresh() {
  loading.value = true
  try {
    const { data } = await getReports(selectedType.value, selectedLimit.value)
    reports.value = data.reports || []
    if (reports.value.length > 0) {
      select(reports.value[0])
    } else {
      activeId.value = null
      active.value = null
    }
  } catch (e) {
    console.error('fetch reports failed', e)
  } finally {
    loading.value = false
  }
}

function select(r) {
  activeId.value = r.id
  active.value = r
}

function fmtTime(t) {
  if (!t) return ''
  const d = new Date(t)
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

onMounted(refresh)
</script>

<style scoped>
.reports-page {
  font-family:
    -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Helvetica Neue', Arial,
    sans-serif;
  background: #fff;
  color: #222;
  min-height: 100vh;
  padding: 24px 32px;
  box-sizing: border-box;
}

.reports-header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  border-bottom: 1px solid #e0e0e0;
  padding-bottom: 12px;
  margin-bottom: 20px;
}

.reports-header h2 {
  font-size: 18px;
  font-weight: 500;
  margin: 0;
  letter-spacing: 0.5px;
}

.reports-filter {
  display: flex;
  gap: 8px;
  align-items: center;
  font-size: 13px;
}

.reports-filter select,
.reports-filter button {
  font: inherit;
  font-size: 13px;
  padding: 4px 10px;
  border: 1px solid #d0d0d0;
  background: #fff;
  color: #222;
  border-radius: 2px;
  cursor: pointer;
}

.reports-filter button:hover {
  background: #f4f4f4;
}

.reports-filter button:disabled {
  color: #999;
  cursor: default;
}

.reports-main {
  display: grid;
  grid-template-columns: 320px 1fr;
  gap: 24px;
  min-height: calc(100vh - 120px);
}

.reports-list {
  border-right: 1px solid #e0e0e0;
  padding-right: 16px;
  max-height: calc(100vh - 120px);
  overflow-y: auto;
}

.reports-item {
  padding: 10px 8px;
  border-bottom: 1px solid #f0f0f0;
  cursor: pointer;
}

.reports-item:hover {
  background: #fafafa;
}

.reports-item.active {
  background: #f4f4f4;
}

.reports-item-time {
  font-size: 11px;
  color: #888;
  font-variant-numeric: tabular-nums;
}

.reports-item-source {
  font-size: 11px;
  color: #666;
  margin-top: 2px;
}

.reports-item-title {
  font-size: 13px;
  color: #222;
  margin-top: 4px;
  line-height: 1.4;
}

.reports-detail article {
  max-width: 820px;
}

.reports-detail h3 {
  font-size: 16px;
  font-weight: 500;
  margin: 0 0 6px 0;
}

.reports-meta {
  font-size: 12px;
  color: #888;
  margin-bottom: 20px;
}

.reports-meta span + span {
  margin-left: 6px;
}

.reports-content {
  font-family:
    'SF Mono', 'Menlo', 'Monaco', 'Consolas', 'Courier New', monospace;
  font-size: 13px;
  line-height: 1.65;
  color: #333;
  background: #fafafa;
  border: 1px solid #eee;
  padding: 18px 20px;
  border-radius: 2px;
  white-space: pre-wrap;
  overflow-x: auto;
  margin: 0;
}

.empty {
  color: #999;
  font-size: 13px;
  padding: 20px 0;
}
</style>
