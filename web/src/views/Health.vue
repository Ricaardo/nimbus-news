<template>
  <div class="page-container">
    <div class="page-header">
      <h1>源健康监控</h1>
      <el-button type="primary" @click="refresh" :loading="loading">
        <el-icon><Refresh /></el-icon>
        刷新
      </el-button>
    </div>

    <el-table :data="sources" v-loading="loading" stripe>
      <el-table-column prop="name" label="名称" width="180" />
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag :type="statusType(row.status)" size="small">
            {{ statusText(row.status) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="成功率" width="120">
        <template #default="{ row }">
          <el-progress
            :percentage="Math.round((row.success_rate || 0) * 100)"
            :color="rateColor(row.success_rate)"
            :stroke-width="12"
          />
        </template>
      </el-table-column>
      <el-table-column prop="consecutive_fails" label="连续失败" width="100" />
      <el-table-column prop="last_error" label="最后错误" min-width="200" show-overflow-tooltip />
      <el-table-column prop="last_success" label="最后成功" width="170">
        <template #default="{ row }">
          {{ row.last_success ? new Date(row.last_success).toLocaleString('zh-CN') : '-' }}
        </template>
      </el-table-column>
      <el-table-column label="操作" width="180">
        <template #default="{ row }">
          <el-button
            link
            type="primary"
            :loading="fetching === row.name"
            :disabled="row.status === 'disabled'"
            @click="fetchNow(row.name)"
          >立即拉取</el-button>
          <el-button link type="primary" @click="reset(row.name)">重置</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 拉取结果弹窗 -->
    <el-dialog
      v-model="fetchDialogVisible"
      :title="`拉取结果 · ${fetchResult?.source || ''}`"
      width="720px"
      top="6vh"
    >
      <div v-if="fetchResult" class="fetch-meta">
        <el-tag size="small" type="success">共 {{ fetchResult.count }} 条</el-tag>
        <span class="fetch-latency">耗时 {{ fetchResult.latency_ms }} ms</span>
      </div>
      <el-empty v-if="fetchResult && fetchResult.count === 0" description="无新消息(可能被去重/过滤拦截)" />
      <el-collapse v-else>
        <el-collapse-item v-for="(m, i) in fetchResult?.messages || []" :key="i">
          <template #title>
            <span class="msg-title">{{ m.title || '(无标题)' }}</span>
            <el-tag size="small" class="msg-source">{{ m.source || m.type }}</el-tag>
          </template>
          <div class="msg-body">
            <pre>{{ m.content || '(无正文)' }}</pre>
            <a v-if="m.link" :href="m.link" target="_blank" rel="noopener" class="msg-link">{{ m.link }}</a>
          </div>
        </el-collapse-item>
      </el-collapse>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { getSourcesHealth, resetSourceHealth, fetchSourceNow } from '../api/config'

const loading = ref(false)
const sources = ref([])
const fetching = ref('')
const fetchDialogVisible = ref(false)
const fetchResult = ref(null)

const refresh = async () => {
  loading.value = true
  try {
    const res = await getSourcesHealth(true)
    sources.value = res.data?.sources || []
  } catch (e) {
    ElMessage.error('获取健康状态失败')
  } finally {
    loading.value = false
  }
}

const reset = async (name) => {
  try {
    await resetSourceHealth(name)
    ElMessage.success(`${name} 已重置`)
    refresh()
  } catch (e) {
    ElMessage.error('重置失败')
  }
}

// 主动拉取:同步执行源 Fetch,展示结果(不推送)
const fetchNow = async (name) => {
  fetching.value = name
  try {
    const res = await fetchSourceNow(name)
    fetchResult.value = res.data
    fetchDialogVisible.value = true
  } catch (e) {
    ElMessage.error(`拉取失败: ${e?.response?.data?.error || e.message || '未知错误'}`)
  } finally {
    fetching.value = ''
  }
}

const statusType = (s) => ({ healthy: 'success', degraded: 'warning', unhealthy: 'danger', disabled: 'info', awaiting_schedule: 'info' }[s] || '')
const statusText = (s) => ({ healthy: '健康', degraded: '降级', unhealthy: '异常', disabled: '禁用', awaiting_schedule: '待调度' }[s] || s)
const rateColor = (r) => ((r || 0) > 0.8 ? '#67C23A' : (r || 0) > 0.5 ? '#E6A23C' : '#F56C6C')

onMounted(refresh)
</script>

<style scoped>
.page-container { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.page-header h1 { font-size: 24px; font-weight: 600; color: #303133; }
.fetch-meta { margin-bottom: 12px; display: flex; align-items: center; gap: 12px; }
.fetch-latency { color: #909399; font-size: 13px; }
.msg-title { font-weight: 500; margin-right: 8px; }
.msg-source { margin-left: auto; }
.msg-body pre {
  white-space: pre-wrap;
  word-break: break-word;
  font-family: inherit;
  font-size: 13px;
  line-height: 1.6;
  color: #303133;
  max-height: 320px;
  overflow-y: auto;
  margin: 0 0 8px;
}
.msg-link { color: #409EFF; font-size: 12px; word-break: break-all; }
</style>
