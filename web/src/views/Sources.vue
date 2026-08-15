<template>
  <div class="page-container">
    <div class="page-header">
      <h1>数据源管理</h1>
      <div>
        <el-button type="primary" @click="showCreateDialog">
          <el-icon><Plus /></el-icon>
          新增数据源
        </el-button>
        <el-button @click="refreshData" :loading="loading">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
      </div>
    </div>

    <el-table :data="sources" v-loading="loading" stripe>
      <el-table-column prop="name" label="名称" width="200" />
      <el-table-column prop="type" label="类型" width="120">
        <template #default="{ row }">
          <el-tag size="small">{{ row.type }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="interval" label="采集间隔" width="100">
        <template #default="{ row }">
          {{ row.interval }}秒
        </template>
      </el-table-column>
      <el-table-column prop="sinks" label="目标渠道">
        <template #default="{ row }">
          <el-tag
            v-for="sink in row.sinks"
            :key="sink"
            size="small"
            type="info"
            style="margin-right: 4px"
          >
            {{ sink }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="status" label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="getStatusType(row.status)" size="small">
            {{ getStatusText(row.status) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="启用" width="80">
        <template #default="{ row }">
          <el-switch
            v-model="row.enabled"
            @change="handleToggle(row)"
            :loading="row.toggling"
          />
        </template>
      </el-table-column>
      <el-table-column label="操作" width="160">
        <template #default="{ row }">
          <el-button link type="primary" @click="editSource(row)">编辑</el-button>
          <el-button link type="danger" @click="confirmDelete(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 新增对话框 -->
    <el-dialog v-model="createVisible" title="新增数据源" width="550px">
      <el-form :model="createForm" label-width="120px">
        <el-form-item label="名称">
          <el-input v-model="createForm.name" placeholder="唯一标识，如 my-rss" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="createForm.type" placeholder="选择类型" @change="onTypeChange">
            <el-option
              v-for="t in sourceTypes"
              :key="t"
              :label="t"
              :value="t"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="采集间隔">
          <el-input-number v-model="createForm.interval" :min="10" :max="3600" />
          <span style="margin-left: 8px">秒</span>
        </el-form-item>
        <el-form-item label="目标渠道">
          <el-select v-model="createForm.sinks" multiple placeholder="选择推送渠道">
            <el-option
              v-for="ch in channelNames"
              :key="ch"
              :label="ch"
              :value="ch"
            />
          </el-select>
        </el-form-item>
        <el-divider content-position="left">选项</el-divider>
        <el-form-item v-if="needsUrl(createForm.type)" label="URL">
          <el-input v-model="createFormOptions.url" placeholder="https://..." />
        </el-form-item>
        <el-form-item label="API Key" v-if="needsApiKey(createForm.type)">
          <el-input v-model="createFormOptions.api_key" type="password" show-password placeholder="API Key" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" @click="createSource" :loading="creating">
          创建
        </el-button>
      </template>
    </el-dialog>

    <!-- 编辑对话框 -->
    <el-dialog v-model="dialogVisible" title="编辑数据源" width="550px">
      <el-form :model="editForm" label-width="120px">
        <el-form-item label="名称">
          <el-input v-model="editForm.name" disabled />
        </el-form-item>
        <el-form-item label="类型">
          <el-input v-model="editForm.type" disabled />
        </el-form-item>
        <el-form-item label="采集间隔">
          <el-input-number v-model="editForm.interval" :min="10" :max="3600" />
          <span style="margin-left: 8px">秒</span>
        </el-form-item>
        <el-form-item label="目标渠道">
          <el-select v-model="editForm.sinks" multiple placeholder="选择推送渠道">
            <el-option v-for="ch in channelNames" :key="ch" :label="ch" :value="ch" />
          </el-select>
        </el-form-item>
        <el-divider content-position="left">选项</el-divider>
        <el-form-item label="URL" v-if="editForm._options?.url !== undefined || editForm.type === 'rss'">
          <el-input v-model="editForm._options.url" placeholder="https://..." />
        </el-form-item>
        <el-form-item label="API Key" v-if="editForm._options?.api_key !== undefined">
          <el-input v-model="editForm._options.api_key" type="password" show-password placeholder="API Key" />
        </el-form-item>
        <el-form-item label="其他选项" v-if="editForm._otherOptions?.length">
          <div v-for="(opt, idx) in editForm._otherOptions" :key="idx" class="option-row">
            <el-input v-model="opt.key" placeholder="key" size="small" style="width: 140px" />
            <el-input v-model="opt.value" placeholder="value" size="small" style="width: 200px; margin-left: 8px" />
            <el-button link type="danger" size="small" @click="editForm._otherOptions.splice(idx, 1)">删除</el-button>
          </div>
        </el-form-item>
        <el-form-item>
          <el-button size="small" @click="editForm._otherOptions.push({ key: '', value: '' })">添加选项</el-button>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="saveSource" :loading="saving">
          保存
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getSources, getSourceTypes, createSource as apiCreateSource, deleteSource as apiDeleteSource, toggleSource, updateSource, getChannels } from '../api/config'

const loading = ref(false)
const saving = ref(false)
const creating = ref(false)
const sources = ref([])
const sourceTypes = ref([])
const channelNames = ref([])
const dialogVisible = ref(false)
const editForm = ref({})
const createVisible = ref(false)
const createForm = ref({ name: '', type: '', interval: 120, sinks: [] })
const createFormOptions = ref({ url: '' })

const refreshData = async () => {
  loading.value = true
  try {
    const [srcRes, chRes] = await Promise.all([
      getSources(),
      getChannels().catch(() => ({ data: { channels: [] } })),
    ])
    sources.value = srcRes.data.sources.map(src => ({ ...src, toggling: false }))
    channelNames.value = (chRes.data?.channels || []).map(c => c.name)
  } catch (err) {
    ElMessage.error('获取数据失败: ' + err.message)
  } finally {
    loading.value = false
  }
}

const loadTypes = async () => {
  try {
    const res = await getSourceTypes()
    sourceTypes.value = res.data?.types || []
  } catch (e) {
    sourceTypes.value = ['rss', 'finnhub', 'fred', 'generic-http', 'jinshi', 'polymarket', 'kalshi', 'guanfu']
  }
}

const onTypeChange = () => {}

const needsUrl = (type) => ['rss', 'generic-http', 'financial-news'].includes(type)
const needsApiKey = (type) => ['finnhub', 'finnhub-ws', 'fred', 'generic-http'].includes(type)

const showCreateDialog = () => {
  createForm.value = { name: '', type: '', interval: 120, sinks: [] }
  createFormOptions.value = { url: '', api_key: '' }
  createVisible.value = true
}

const createSource = async () => {
  if (!createForm.value.name || !createForm.value.type) {
    ElMessage.error('名称和类型为必填项')
    return
  }
  creating.value = true
  try {
    const body = { ...createForm.value }
    const options = {}
    if (createFormOptions.value.url) body.url = createFormOptions.value.url
    if (createFormOptions.value.api_key) options.api_key = createFormOptions.value.api_key
    if (Object.keys(options).length > 0) body.options = options
    await apiCreateSource(body)
    ElMessage.success('数据源已创建')
    createVisible.value = false
    await refreshData()
  } catch (err) {
    ElMessage.error('创建失败: ' + err.message)
  } finally {
    creating.value = false
  }
}

const confirmDelete = (source) => {
  ElMessageBox.confirm(`确定删除数据源 "${source.name}"？`, '确认删除', {
    confirmButtonText: '删除',
    cancelButtonText: '取消',
    type: 'warning',
  }).then(() => doDelete(source.name))
}

const doDelete = async (name) => {
  try {
    await apiDeleteSource(name)
    ElMessage.success(`数据源 ${name} 已删除`)
    await refreshData()
  } catch (err) {
    ElMessage.error('删除失败: ' + err.message)
  }
}

const handleToggle = async (source) => {
  source.toggling = true
  try {
    await toggleSource(source.name, source.enabled)
    ElMessage.success(`数据源 ${source.name} 已${source.enabled ? '启用' : '禁用'}`)
    await refreshData()
  } catch (err) {
    source.enabled = !source.enabled
    ElMessage.error('操作失败: ' + err.message)
  } finally {
    source.toggling = false
  }
}

const editSource = (source) => {
  const opts = source.options || source._options || {}
  const otherOpts = []
  for (const [k, v] of Object.entries(opts)) {
    if (k !== 'url' && k !== 'api_key') {
      otherOpts.push({
        key: k,
        value: typeof v === 'string' ? v : JSON.stringify(v),
        originalValue: v,
      })
    }
  }
  editForm.value = {
    ...source,
    _options: {
      url: source.url || '',
      api_key: opts.api_key || '',
    },
    _otherOptions: otherOpts,
  }
  dialogVisible.value = true
}

const saveSource = async () => {
  saving.value = true
  try {
    const { _options, _otherOptions, status, toggling, ...body } = editForm.value
    body.url = _options?.url || ''
    const options = {}
    if (_options?.api_key && _options.api_key !== '***') options.api_key = _options.api_key
    for (const opt of (_otherOptions || [])) {
      if (!opt.key) continue
	  if (opt.value === '***') continue
      const originalText = typeof opt.originalValue === 'string' ? opt.originalValue : JSON.stringify(opt.originalValue)
      if (opt.value === originalText) {
        options[opt.key] = opt.originalValue
        continue
      }
      try {
        options[opt.key] = JSON.parse(opt.value)
      } catch {
        options[opt.key] = opt.value
      }
    }
    if (Object.keys(options).length > 0) body.options = options
    await updateSource(editForm.value.name, body)
    ElMessage.success('保存成功')
    dialogVisible.value = false
    await refreshData()
  } catch (err) {
    ElMessage.error('保存失败: ' + err.message)
  } finally {
    saving.value = false
  }
}

const getStatusType = (s) => ({ running: 'success', stopped: 'warning', disabled: 'info' }[s] || '')
const getStatusText = (s) => ({ running: '运行中', stopped: '已停止', disabled: '已禁用', unknown: '未知' }[s] || s || '未知')

onMounted(() => { refreshData(); loadTypes() })
</script>

<style scoped>
.page-container { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.page-header h1 { font-size: 24px; font-weight: 600; color: #303133; }
</style>
