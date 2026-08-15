<template>
  <div class="page-container">
    <div class="page-header">
      <h1>仪表盘</h1>
      <el-tag type="info" size="small">每 30s 自动刷新</el-tag>
    </div>

    <!-- 统计卡片 -->
    <el-row :gutter="20">
      <el-col :span="6">
        <el-card class="stat-card">
          <div class="stat-value">{{ status.enabledSources || 0 }}</div>
          <div class="stat-label">活跃数据源</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card">
          <div class="stat-value">{{ status.enabledChannels || 0 }}</div>
          <div class="stat-label">活跃渠道</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card">
          <div class="stat-value">{{ healthSummary?.healthy || 0 }}</div>
          <div class="stat-label">健康源</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card">
          <div class="stat-value">{{ messageCount }}</div>
          <div class="stat-label">缓存消息</div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 健康概览 -->
    <el-row :gutter="20" style="margin-top: 20px">
      <el-col :span="12">
        <el-card>
          <template #header>
            <span>数据源健康</span>
          </template>
          <div v-if="healthSummary" class="health-bar">
            <div class="bar-row">
              <span class="bar-label">健康</span>
              <el-progress
                :percentage="pct(healthSummary.healthy, healthSummary.total)"
                :color="'#67C23A'"
                :stroke-width="16"
              />
              <span class="bar-num">{{ healthSummary.healthy }}</span>
            </div>
            <div class="bar-row">
              <span class="bar-label">降级</span>
              <el-progress
                :percentage="pct(healthSummary.degraded, healthSummary.total)"
                :color="'#E6A23C'"
                :stroke-width="16"
              />
              <span class="bar-num">{{ healthSummary.degraded }}</span>
            </div>
            <div class="bar-row">
              <span class="bar-label">异常</span>
              <el-progress
                :percentage="pct(healthSummary.unhealthy, healthSummary.total)"
                :color="'#F56C6C'"
                :stroke-width="16"
              />
              <span class="bar-num">{{ healthSummary.unhealthy }}</span>
            </div>
            <div class="bar-row">
              <span class="bar-label">待调度</span>
              <el-progress
                :percentage="pct(healthSummary.awaiting_schedule || 0, healthSummary.total)"
                :color="'#909399'"
                :stroke-width="16"
              />
              <span class="bar-num">{{ healthSummary.awaiting_schedule || 0 }}</span>
            </div>
          </div>
          <el-empty v-else description="健康监控未启用" :image-size="80" />
        </el-card>
      </el-col>

      <el-col :span="12">
        <el-card>
          <template #header>
            <span>服务信息</span>
          </template>
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item label="服务">{{ status.server?.name || '-' }}</el-descriptions-item>
            <el-descriptions-item label="端口">{{ status.server?.port || '-' }}</el-descriptions-item>
            <el-descriptions-item label="配置">{{ status.config?.path || '-' }}</el-descriptions-item>
            <el-descriptions-item label="最后修改">{{ formatTime(status.config?.lastModified) }}</el-descriptions-item>
          </el-descriptions>
        </el-card>
      </el-col>
    </el-row>

    <!-- 最近消息 -->
    <el-card style="margin-top: 20px">
      <template #header>
        <span>最近消息</span>
        <el-button link type="primary" @click="loadMessages" style="float: right">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
      </template>
      <el-table :data="messages" size="small" max-height="400">
        <el-table-column prop="title" label="标题" min-width="200" show-overflow-tooltip />
        <el-table-column prop="source" label="来源" width="140" />
        <el-table-column label="时间" width="170">
          <template #default="{ row }">
            {{ formatTime(row.create_time || row.fetch_time) }}
          </template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { getStatus, getSourcesHealth, getDebugMessages } from '../api/config'

const status = ref({})
const healthSummary = ref(null)
const messages = ref([])
const messageCount = ref(0)
let timer = null

const loadAll = async () => {
  try {
    const [s, h, m] = await Promise.all([
      getStatus().catch(() => ({ data: {} })),
      getSourcesHealth().catch(() => ({ data: {} })),
      getDebugMessages().catch(() => ({ data: { messages: [], count: 0 } })),
    ])
    status.value = s.data
    healthSummary.value = h.data?.summary
    messages.value = m.data?.messages || []
    messageCount.value = m.data?.count || m.data?.messages?.length || 0
  } catch (e) {
    // silently fail on refresh
  }
}

const loadMessages = loadAll

const pct = (val, total) => (total ? Math.round((val / total) * 100) : 0)
const formatTime = (t) => {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN')
}

onMounted(() => {
  loadAll()
  timer = setInterval(loadAll, 30000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.page-container { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.page-header h1 { font-size: 24px; font-weight: 600; color: #303133; }
.stat-card { text-align: center; }
.stat-value { font-size: 36px; font-weight: 700; color: #409EFF; }
.stat-label { font-size: 14px; color: #909399; margin-top: 4px; }
.health-bar { padding: 10px 0; }
.bar-row { display: flex; align-items: center; margin-bottom: 12px; }
.bar-label { width: 40px; font-size: 13px; color: #606266; }
.bar-row .el-progress { flex: 1; margin: 0 12px; }
.bar-num { width: 30px; text-align: right; font-size: 13px; color: #909399; }
</style>
