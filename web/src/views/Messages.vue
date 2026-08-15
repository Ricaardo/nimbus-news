<template>
  <div class="page-container">
    <div class="page-header">
      <h1>消息日志</h1>
      <el-button type="primary" @click="refresh" :loading="loading">
        <el-icon><Refresh /></el-icon>
        刷新
      </el-button>
    </div>

    <el-row :gutter="20" style="margin-bottom: 16px">
      <el-col :span="6">
        <el-input v-model="filterSource" placeholder="按来源筛选" clearable @change="applyFilter" />
      </el-col>
      <el-col :span="6">
        <el-input v-model="filterKeyword" placeholder="搜索关键词" clearable @change="applyFilter" />
      </el-col>
    </el-row>

    <el-table :data="displayMessages" v-loading="loading" stripe max-height="600">
      <el-table-column label="时间" width="170">
        <template #default="{ row }">
          {{ formatTime(row.create_time || row.fetch_time) }}
        </template>
      </el-table-column>
      <el-table-column prop="source" label="来源" width="150" />
      <el-table-column prop="title" label="标题" min-width="250" show-overflow-tooltip />
      <el-table-column label="AI 评分" width="100">
        <template #default="{ row }">
          <el-tag v-if="row.metadata?.ai_score != null" :type="scoreType(row.metadata.ai_score)" size="small">
            {{ row.metadata.ai_score }}
          </el-tag>
          <span v-else>-</span>
        </template>
      </el-table-column>
      <el-table-column label="链接" width="80">
        <template #default="{ row }">
          <el-button v-if="row.link" link type="primary" size="small" @click="openLink(row.link)">
            打开
          </el-button>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { getDebugMessages } from '../api/config'

const loading = ref(false)
const messages = ref([])
const filterSource = ref('')
const filterKeyword = ref('')

const displayMessages = computed(() => {
  let msgs = messages.value
  if (filterSource.value) {
    msgs = msgs.filter(m => (m.source || '').includes(filterSource.value))
  }
  if (filterKeyword.value) {
    const kw = filterKeyword.value.toLowerCase()
    msgs = msgs.filter(m =>
      (m.title || '').toLowerCase().includes(kw) ||
      (m.content || '').toLowerCase().includes(kw)
    )
  }
  return msgs
})

const applyFilter = () => {}

const refresh = async () => {
  loading.value = true
  try {
    const res = await getDebugMessages()
    messages.value = res.data?.messages || []
  } catch (e) {
    // ignore
  } finally {
    loading.value = false
  }
}

const scoreType = (s) => (s >= 8 ? 'danger' : s >= 6 ? 'warning' : 'info')
const formatTime = (t) => t ? new Date(t).toLocaleString('zh-CN') : '-'
const openLink = (url) => window.open(url, '_blank')

onMounted(refresh)
</script>

<style scoped>
.page-container { padding: 20px; }
.page-header { display: flex; justify-space: space-between; align-items: center; margin-bottom: 20px; }
.page-header h1 { font-size: 24px; font-weight: 600; color: #303133; }
</style>
