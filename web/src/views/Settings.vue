<template>
  <div class="page-container">
    <div class="page-header">
      <h1>系统设置</h1>
      <div>
        <el-button @click="refreshAll" :loading="loading">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
        <el-button type="warning" @click="reloadConfig" :loading="reloading">
          <el-icon><RefreshRight /></el-icon>
          重新加载配置
        </el-button>
      </div>
    </div>

    <el-tabs v-model="activeTab" type="border-card">
      <!-- 系统状态 -->
      <el-tab-pane label="系统状态" name="status">
        <el-row :gutter="20">
          <el-col :span="12">
            <el-descriptions :column="1" border title="服务信息">
              <el-descriptions-item label="服务名称">{{ status.server?.name || '-' }}</el-descriptions-item>
              <el-descriptions-item label="服务端口">{{ status.server?.port || '-' }}</el-descriptions-item>
              <el-descriptions-item label="配置文件">{{ status.config?.path || '-' }}</el-descriptions-item>
              <el-descriptions-item label="最后修改">{{ formatTime(status.config?.lastModified) }}</el-descriptions-item>
            </el-descriptions>
          </el-col>
          <el-col :span="12">
            <el-descriptions :column="1" border title="运行统计">
              <el-descriptions-item label="渠道总数">
                {{ status.channels || 0 }}
                <el-tag size="small" type="success" style="margin-left: 8px">启用: {{ status.enabledChannels || 0 }}</el-tag>
              </el-descriptions-item>
              <el-descriptions-item label="数据源总数">
                {{ status.sources || 0 }}
                <el-tag size="small" type="success" style="margin-left: 8px">启用: {{ status.enabledSources || 0 }}</el-tag>
              </el-descriptions-item>
            </el-descriptions>
          </el-col>
        </el-row>
      </el-tab-pane>

      <!-- 过滤器配置 -->
      <el-tab-pane label="过滤器" name="filters">
        <el-form :model="filtersForm" label-width="140px">
          <!-- 去重 -->
          <el-divider content-position="left">去重设置</el-divider>
          <el-form-item label="启用去重">
            <el-switch v-model="filtersForm.dedup.enabled" />
          </el-form-item>
          <el-form-item label="去重 TTL (秒)">
            <el-input-number v-model="filtersForm.dedup.ttl" :min="60" :max="604800" />
          </el-form-item>
          <el-form-item label="语义去重">
            <el-switch v-model="filtersForm.dedup.semantic.enabled" />
          </el-form-item>
          <el-form-item v-if="filtersForm.dedup.semantic.enabled" label="相似度阈值">
            <el-input-number v-model="filtersForm.dedup.semantic.threshold" :min="0.1" :max="1.0" :step="0.05" :precision="2" />
          </el-form-item>
          <el-form-item v-if="filtersForm.dedup.semantic.enabled" label="时间窗口 (秒)">
            <el-input-number v-model="filtersForm.dedup.semantic.time_window" :min="60" :max="86400" />
          </el-form-item>
          <el-form-item label="跨源去重组">
            <div v-for="(group, gi) in filtersForm.dedup.groups" :key="gi" class="group-row">
              <el-select v-model="filtersForm.dedup.groups[gi]" multiple placeholder="选择归入同一组的源" style="width: 400px">
                <el-option v-for="s in sourceNames" :key="s" :label="s" :value="s" />
              </el-select>
              <el-button link type="danger" @click="filtersForm.dedup.groups.splice(gi, 1)">删除</el-button>
            </div>
            <el-button size="small" @click="addDedupGroup">添加去重组</el-button>
          </el-form-item>
          <el-form-item label="跳过去重的渠道">
            <el-select v-model="filtersForm.dedup.skip_sinks" multiple placeholder="选择跳过去重的渠道" clearable>
              <el-option v-for="ch in channelNames" :key="ch" :label="ch" :value="ch" />
            </el-select>
          </el-form-item>

          <!-- AI 过滤器 -->
          <el-divider content-position="left">AI 过滤器</el-divider>
          <el-form-item label="启用 AI 过滤">
            <el-switch v-model="filtersForm.ai_filter.enabled" />
          </el-form-item>
          <el-form-item label="过滤阈值 (0-10)">
            <el-input-number v-model="filtersForm.ai_filter.threshold_score" :min="0" :max="10" :step="0.5" :precision="1" />
            <span class="form-hint">低于此分数的新闻被过滤</span>
          </el-form-item>
          <el-form-item label="每分钟最大处理数">
            <el-input-number v-model="filtersForm.ai_filter.max_per_minute" :min="1" :max="100" />
          </el-form-item>
          <el-form-item label="屏蔽分类">
            <el-select v-model="filtersForm.ai_filter.block_categories" multiple placeholder="选择屏蔽的分类" clearable>
              <el-option label="垃圾广告" value="spam" />
              <el-option label="低质内容" value="low_quality" />
              <el-option label="重复内容" value="duplicate" />
              <el-option label="无关内容" value="irrelevant" />
            </el-select>
          </el-form-item>
          <el-form-item label="目标数据源">
            <el-select v-model="filtersForm.ai_filter.target_sources" multiple placeholder="留空表示所有源" clearable>
              <el-option v-for="s in sourceNames" :key="s" :label="s" :value="s" />
            </el-select>
          </el-form-item>

          <!-- 内容过滤 -->
          <el-divider content-position="left">内容过滤</el-divider>
          <el-form-item label="启用内容过滤">
            <el-switch v-model="filtersForm.content.enabled" />
          </el-form-item>
          <el-form-item label="最小内容长度">
            <el-input-number v-model="filtersForm.content.min_content_length" :min="0" :max="5000" />
          </el-form-item>
          <el-form-item label="屏蔽关键词">
            <el-select v-model="filtersForm.content.block_keywords" multiple filterable allow-create placeholder="输入关键词后回车添加" clearable style="width: 100%" />
          </el-form-item>
          <el-form-item label="严格过滤源">
            <el-select v-model="filtersForm.content.strict_sources" multiple placeholder="选择严格过滤的源" clearable>
              <el-option v-for="s in sourceNames" :key="s" :label="s" :value="s" />
            </el-select>
          </el-form-item>

          <!-- 频控 -->
          <el-divider content-position="left">频率控制</el-divider>
          <el-form-item label="启用频控">
            <el-switch v-model="filtersForm.ratelimit.enabled" />
          </el-form-item>
          <el-form-item v-if="filtersForm.ratelimit.enabled" label="频控规则">
            <div class="rules-container">
              <div v-for="(rule, ri) in filtersForm.ratelimit.rules" :key="ri" class="rule-row">
                <el-select v-model="rule.source" placeholder="源 (*=所有)" size="small" style="width: 140px" clearable>
                  <el-option label="* (所有源)" value="*" />
                  <el-option v-for="s in sourceNames" :key="s" :label="s" :value="s" />
                </el-select>
                <el-select v-model="rule.sink" placeholder="渠道 (*=所有)" size="small" style="width: 140px; margin-left: 6px" clearable>
                  <el-option label="* (所有渠道)" value="*" />
                  <el-option v-for="ch in channelNames" :key="ch" :label="ch" :value="ch" />
                </el-select>
                <el-input-number v-model="rule.max_per_minute" :min="1" :max="100" size="small" style="width: 100px; margin-left: 6px" />
                <span style="margin-left: 4px; font-size: 12px; color: #909399">条/分钟</span>
                <el-button link type="danger" size="small" @click="filtersForm.ratelimit.rules.splice(ri, 1)">删除</el-button>
              </div>
              <el-button size="small" @click="addRatelimitRule">添加规则</el-button>
            </div>
          </el-form-item>

          <el-form-item>
            <el-button type="primary" @click="saveFilters" :loading="savingFilters">保存过滤器配置</el-button>
          </el-form-item>
        </el-form>
      </el-tab-pane>

      <!-- 告警配置 -->
      <el-tab-pane label="告警" name="alert">
        <el-form :model="alertForm" label-width="160px">
          <el-form-item label="启用告警">
            <el-switch v-model="alertForm.enabled" />
          </el-form-item>
          <el-form-item label="检查间隔 (秒)">
            <el-input-number v-model="alertForm.check_interval" :min="5" :max="3600" />
          </el-form-item>
          <el-form-item label="每用户最大告警数">
            <el-input-number v-model="alertForm.max_alerts_per_user" :min="1" :max="500" />
          </el-form-item>
          <el-form-item label="默认冷却时间 (秒)">
            <el-input-number v-model="alertForm.default_cooldown" :min="10" :max="86400" />
          </el-form-item>
          <el-form-item>
            <el-button type="primary" @click="saveAlert" :loading="savingAlert">保存告警配置</el-button>
          </el-form-item>
        </el-form>
      </el-tab-pane>

      <!-- 健康监控 -->
      <el-tab-pane label="健康监控" name="health">
        <el-form :model="healthForm" label-width="180px">
          <el-form-item label="启用健康监控">
            <el-switch v-model="healthForm.enabled" />
          </el-form-item>
          <el-form-item label="降级阈值 (连续失败次数)">
            <el-input-number v-model="healthForm.degraded_threshold" :min="1" :max="100" />
          </el-form-item>
          <el-form-item label="不健康阈值 (连续失败次数)">
            <el-input-number v-model="healthForm.unhealthy_threshold" :min="1" :max="100" />
          </el-form-item>
          <el-form-item label="恢复探测间隔 (秒)">
            <el-input-number v-model="healthForm.recovery_interval" :min="30" :max="3600" />
          </el-form-item>
          <el-form-item label="通知渠道">
            <el-select v-model="healthForm.notify_channels" multiple placeholder="选择通知渠道">
              <el-option v-for="ch in channelNames" :key="ch" :label="ch" :value="ch" />
            </el-select>
          </el-form-item>
          <el-form-item>
            <el-button type="primary" @click="saveHealth" :loading="savingHealth">保存健康监控配置</el-button>
          </el-form-item>
        </el-form>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import {
  getStatus, reloadConfig as apiReloadConfig,
  getFiltersConfig, updateFiltersConfig,
  getAlertConfig, updateAlertConfig,
  getHealthConfig, updateHealthConfig,
  getSources, getChannels,
} from '../api/config'

const loading = ref(false)
const reloading = ref(false)
const activeTab = ref('status')
const status = ref({})
const sourceNames = ref([])
const channelNames = ref([])

const savingFilters = ref(false)
const savingAlert = ref(false)
const savingHealth = ref(false)

const filtersForm = reactive({
  dedup: { enabled: true, ttl: 86400, groups: [], skip_sinks: [], semantic: { enabled: false, threshold: 0.6, time_window: 3600, cache_size: 1000 } },
  ai_filter: { enabled: false, threshold_score: 5, max_per_minute: 10, block_categories: [], target_sources: [] },
  content: { enabled: false, min_content_length: 0, block_keywords: [], strict_sources: [] },
  ratelimit: { enabled: false, rules: [] },
})

const alertForm = reactive({
  enabled: false, check_interval: 30, max_alerts_per_user: 50, default_cooldown: 300,
})

const healthForm = reactive({
  enabled: false, degraded_threshold: 3, unhealthy_threshold: 5, recovery_interval: 300, notify_channels: [],
})

const refreshAll = async () => {
  loading.value = true
  try {
    const [sts, filters, alert, health, srcRes, chRes] = await Promise.all([
      getStatus(),
      getFiltersConfig().catch(() => ({ data: {} })),
      getAlertConfig().catch(() => ({ data: {} })),
      getHealthConfig().catch(() => ({ data: {} })),
      getSources().catch(() => ({ data: { sources: [] } })),
      getChannels().catch(() => ({ data: { channels: [] } })),
    ])
    status.value = sts.data
    sourceNames.value = (srcRes.data?.sources || []).map(s => s.name)
    channelNames.value = (chRes.data?.channels || []).map(c => c.name)

    if (filters.data?.filters) {
      const f = filters.data.filters
      filtersForm.dedup = { enabled: true, ttl: 86400, groups: [], skip_sinks: [], semantic: { enabled: false, threshold: 0.6, time_window: 3600, cache_size: 1000 }, ...f.dedup }
      if (f.dedup?.semantic) Object.assign(filtersForm.dedup.semantic, f.dedup.semantic)
      if (!filtersForm.dedup.groups) filtersForm.dedup.groups = []
      if (!filtersForm.dedup.skip_sinks) filtersForm.dedup.skip_sinks = []
      filtersForm.ai_filter = { enabled: false, threshold_score: 5, max_per_minute: 10, block_categories: [], target_sources: [], ...f.ai_filter }
      filtersForm.content = { enabled: false, min_content_length: 0, block_keywords: [], strict_sources: [], ...f.content }
      filtersForm.ratelimit = { enabled: false, rules: [], ...f.ratelimit }
    }
    if (alert.data?.alert) Object.assign(alertForm, alert.data.alert)
    if (health.data?.health) Object.assign(healthForm, health.data.health)
  } catch (err) {
    ElMessage.error('获取数据失败: ' + err.message)
  } finally {
    loading.value = false
  }
}

const reloadConfig = async () => {
  reloading.value = true
  try {
    await apiReloadConfig()
    ElMessage.success('配置已重新加载')
    await refreshAll()
  } catch (err) {
    ElMessage.error('重新加载失败: ' + err.message)
  } finally {
    reloading.value = false
  }
}

const saveFilters = async () => {
  savingFilters.value = true
  try {
    await updateFiltersConfig({ ...filtersForm })
    ElMessage.success('过滤器配置已保存')
  } catch (err) {
    ElMessage.error('保存失败: ' + (err.response?.data?.error || err.message))
  } finally {
    savingFilters.value = false
  }
}

const saveAlert = async () => {
  savingAlert.value = true
  try {
    await updateAlertConfig({ ...alertForm })
    ElMessage.success('告警配置已保存')
  } catch (err) {
    ElMessage.error('保存失败: ' + (err.response?.data?.error || err.message))
  } finally {
    savingAlert.value = false
  }
}

const saveHealth = async () => {
  savingHealth.value = true
  try {
    await updateHealthConfig({ ...healthForm })
    ElMessage.success('健康监控配置已保存')
  } catch (err) {
    ElMessage.error('保存失败: ' + (err.response?.data?.error || err.message))
  } finally {
    savingHealth.value = false
  }
}
const addDedupGroup = () => {
  if (!filtersForm.dedup.groups) filtersForm.dedup.groups = []
  filtersForm.dedup.groups.push([])
}

const addRatelimitRule = () => {
  if (!filtersForm.ratelimit.rules) filtersForm.ratelimit.rules = []
  filtersForm.ratelimit.rules.push({ source: '*', sink: '*', max_per_minute: 10 })
}

const formatTime = (time) => {
  if (!time) return '-'
  return new Date(time).toLocaleString('zh-CN')
}

onMounted(refreshAll)
</script>

<style scoped>
.page-container { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.page-header h1 { font-size: 24px; font-weight: 600; color: #303133; }
.form-hint { margin-left: 8px; font-size: 12px; color: #909399; }
.el-tabs { background: #fff; }
.el-tab-pane { padding: 20px; }
</style>
