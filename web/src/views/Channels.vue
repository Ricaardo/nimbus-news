<template>
  <div class="page-container">
    <div class="page-header">
      <h1>渠道管理</h1>
      <div>
        <el-button type="primary" @click="showCreateDialog">
          <el-icon><Plus /></el-icon>
          新增渠道
        </el-button>
        <el-button @click="refreshData" :loading="loading">
          <el-icon><Refresh /></el-icon>
          刷新
        </el-button>
      </div>
    </div>

    <el-row :gutter="20">
      <el-col :span="8" v-for="channel in channels" :key="channel.name">
        <el-card class="channel-card" :class="{ disabled: !channel.enabled }" @click="editChannel(channel)">
          <template #header>
            <div class="card-header">
              <span class="channel-name">{{ channel.name }}</span>
              <el-switch
                v-model="channel.enabled"
                @change="handleToggle(channel)"
                @click.stop
                :loading="channel.toggling"
              />
            </div>
          </template>

          <div class="channel-info">
            <p>
              <el-tag size="small">{{ channel.type }}</el-tag>
              <el-tag size="small" type="info">{{ channel.mode }}</el-tag>
            </p>
            <p class="status">
              状态:
              <el-tag
                :type="getStatusType(channel.status)"
                size="small"
              >
                {{ getStatusText(channel.status) }}
              </el-tag>
            </p>
            <p class="hint">点击卡片编辑配置</p>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-empty v-if="channels.length === 0 && !loading" description="暂无渠道" />

    <!-- 新增对话框 -->
    <el-dialog v-model="createVisible" title="新增渠道" width="550px">
      <el-form :model="createForm" label-width="120px">
        <el-form-item label="名称">
          <el-input v-model="createForm.name" placeholder="唯一标识，如 wechat-main" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="createForm.type" placeholder="选择类型" @change="onCreateTypeChange">
            <el-option label="微信" value="wechat" />
            <el-option label="Telegram" value="telegram" />
            <el-option label="Discord" value="discord" />
            <el-option label="REST" value="rest" />
          </el-select>
        </el-form-item>
        <el-form-item label="模式">
          <el-select v-model="createForm.mode">
            <el-option label="仅推送" value="push" />
            <el-option label="仅接收" value="receive" />
            <el-option label="双向" value="bidirectional" />
          </el-select>
        </el-form-item>
        <el-form-item label="Webhook URL">
          <el-input v-model="createForm.webhook" placeholder="https://..." />
        </el-form-item>
        <el-divider content-position="left">配置项</el-divider>
        <el-form-item label="Bot Token" v-if="createOptVisible('bot_token')">
          <el-input v-model="createOptions.bot_token" type="password" show-password placeholder="Bot Token" />
        </el-form-item>
        <el-form-item label="App ID" v-if="createOptVisible('app_id')">
          <el-input v-model="createOptions.app_id" placeholder="App ID" />
        </el-form-item>
        <el-form-item label="App Secret" v-if="createOptVisible('app_secret')">
          <el-input v-model="createOptions.app_secret" type="password" show-password placeholder="App Secret" />
        </el-form-item>
        <el-form-item label="Encrypt Key" v-if="createOptVisible('encrypt_key')">
          <el-input v-model="createOptions.encrypt_key" type="password" show-password placeholder="Encrypt Key" />
        </el-form-item>
        <el-form-item label="Verification Token" v-if="createOptVisible('verification_token')">
          <el-input v-model="createOptions.verification_token" type="password" show-password placeholder="Verification Token" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" @click="createChannel" :loading="creating">创建</el-button>
      </template>
    </el-dialog>

    <!-- 编辑对话框 -->
    <el-dialog v-model="dialogVisible" title="编辑渠道" width="550px">
      <el-form :model="editForm" label-width="120px">
        <el-form-item label="名称">
          <el-input :model-value="editForm.name" disabled />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="editForm.type">
            <el-option label="微信" value="wechat" />
            <el-option label="Telegram" value="telegram" />
            <el-option label="Discord" value="discord" />
            <el-option label="REST" value="rest" />
          </el-select>
        </el-form-item>
        <el-form-item label="模式">
          <el-select v-model="editForm.mode">
            <el-option label="仅推送" value="push" />
            <el-option label="仅接收" value="receive" />
            <el-option label="双向" value="bidirectional" />
          </el-select>
        </el-form-item>
        <el-form-item label="Webhook URL">
          <el-input v-model="editForm.webhook" placeholder="https://..." />
        </el-form-item>
        <el-divider content-position="left">配置项</el-divider>
        <el-form-item label="Bot Token" v-if="showOpt(editForm.type, 'bot_token')">
          <el-input v-model="editForm._options.bot_token" type="password" show-password placeholder="Bot Token" />
        </el-form-item>
        <el-form-item label="App ID" v-if="showOpt(editForm.type, 'app_id')">
          <el-input v-model="editForm._options.app_id" placeholder="App ID" />
        </el-form-item>
        <el-form-item label="App Secret" v-if="showOpt(editForm.type, 'app_secret')">
          <el-input v-model="editForm._options.app_secret" type="password" show-password placeholder="App Secret" />
        </el-form-item>
        <el-form-item label="Encrypt Key" v-if="showOpt(editForm.type, 'encrypt_key')">
          <el-input v-model="editForm._options.encrypt_key" type="password" show-password placeholder="Encrypt Key" />
        </el-form-item>
        <el-form-item label="Verification Token" v-if="showOpt(editForm.type, 'verification_token')">
          <el-input v-model="editForm._options.verification_token" type="password" show-password placeholder="Verification Token" />
        </el-form-item>
        <el-form-item label="其他配置" v-if="editForm._otherOptions?.length">
          <div v-for="(opt, idx) in editForm._otherOptions" :key="idx" class="option-row">
            <el-input v-model="opt.key" placeholder="key" size="small" style="width: 140px" />
            <el-input v-model="opt.value" placeholder="value" size="small" style="width: 200px; margin-left: 8px" />
            <el-button link type="danger" size="small" @click="editForm._otherOptions.splice(idx, 1)">删除</el-button>
          </div>
        </el-form-item>
        <el-form-item>
          <el-button size="small" @click="addOtherOption">添加配置项</el-button>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="saveChannel" :loading="saving">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { getChannels, createChannel as apiCreateChannel, getChannel, updateChannel, toggleChannel } from '../api/config'

const loading = ref(false)
const saving = ref(false)
const creating = ref(false)
const channels = ref([])
const createVisible = ref(false)
const createForm = ref({ name: '', type: '', mode: 'push', webhook: '' })
const createOptions = ref({})
const dialogVisible = ref(false)
const editForm = ref({})

const knownOpts = ['bot_token', 'app_id', 'app_secret', 'encrypt_key', 'verification_token']

const refreshData = async () => {
  loading.value = true
  try {
    const res = await getChannels()
    channels.value = res.data.channels.map(ch => ({
      ...ch,
      toggling: false
    }))
  } catch (err) {
    ElMessage.error('获取渠道列表失败: ' + err.message)
  } finally {
    loading.value = false
  }
}

const handleToggle = async (channel) => {
  channel.toggling = true
  try {
    await toggleChannel(channel.name, channel.enabled)
    ElMessage.success(`渠道 ${channel.name} 已${channel.enabled ? '启用' : '禁用'}`)
    await refreshData()
  } catch (err) {
    channel.enabled = !channel.enabled
    ElMessage.error('操作失败: ' + err.message)
  } finally {
    channel.toggling = false
  }
}

const showCreateDialog = () => {
  createForm.value = { name: '', type: '', mode: 'push', webhook: '' }
  createOptions.value = {}
  createVisible.value = true
}

const onCreateTypeChange = () => {
  createOptions.value = {}
}

const createOptVisible = (key) => {
  return showOpt(createForm.value.type, key)
}

const createChannel = async () => {
  if (!createForm.value.name || !createForm.value.type) {
    ElMessage.error('名称和类型为必填项')
    return
  }
  creating.value = true
  try {
    const body = { ...createForm.value }
    const opts = {}
    for (const [k, v] of Object.entries(createOptions.value)) {
      if (v) opts[k] = v
    }
    if (Object.keys(opts).length > 0) body.options = opts
    await apiCreateChannel(body)
    ElMessage.success('渠道已创建')
    createVisible.value = false
    await refreshData()
  } catch (err) {
    ElMessage.error('创建失败: ' + (err.response?.data?.error || err.message))
  } finally {
    creating.value = false
  }
}

const editChannel = async (channel) => {
  try {
    const res = await getChannel(channel.name)
    const ch = res.data.channel
    const opts = ch.options || {}
    const known = {}
    const other = []
    for (const [k, v] of Object.entries(opts)) {
      if (knownOpts.includes(k)) {
        known[k] = typeof v === 'string' ? v : JSON.stringify(v)
      } else {
        other.push({ key: k, value: typeof v === 'string' ? v : JSON.stringify(v) })
      }
    }
    editForm.value = {
      name: ch.name,
      type: ch.type,
      mode: ch.mode,
      webhook: ch.webhook || '',
      _options: known,
      _otherOptions: other,
    }
    dialogVisible.value = true
  } catch (err) {
    ElMessage.error('获取渠道详情失败: ' + err.message)
  }
}

const showOpt = (type, key) => {
  const map = {
    wechat: ['bot_token', 'app_id', 'app_secret'],
    telegram: ['bot_token'],
    discord: ['bot_token'],
  }
  return (map[type] || []).includes(key)
}

const addOtherOption = () => {
  if (!editForm.value._otherOptions) editForm.value._otherOptions = []
  editForm.value._otherOptions.push({ key: '', value: '' })
}

const saveChannel = async () => {
  saving.value = true
  try {
    const body = {
      name: editForm.value.name,
      type: editForm.value.type,
      mode: editForm.value.mode,
      webhook: editForm.value.webhook || '',
    }
    const options = {}
    for (const key of knownOpts) {
      if (editForm.value._options[key]) options[key] = editForm.value._options[key]
    }
    for (const opt of (editForm.value._otherOptions || [])) {
      if (opt.key) options[opt.key] = opt.value
    }
    if (Object.keys(options).length > 0) body.options = options
    await updateChannel(editForm.value.name, body)
    ElMessage.success('渠道配置已保存')
    dialogVisible.value = false
    await refreshData()
  } catch (err) {
    ElMessage.error('保存失败: ' + (err.response?.data?.error || err.message))
  } finally {
    saving.value = false
  }
}

const getStatusType = (status) => {
  switch (status) {
    case 'running': return 'success'
    case 'stopped': return 'warning'
    case 'disabled': return 'info'
    default: return ''
  }
}

const getStatusText = (status) => {
  switch (status) {
    case 'running': return '运行中'
    case 'stopped': return '已停止'
    case 'disabled': return '已禁用'
    default: return '未知'
  }
}

onMounted(refreshData)
</script>

<style scoped>
.page-container {
  padding: 20px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.page-header h1 {
  font-size: 24px;
  font-weight: 600;
  color: #303133;
}

.channel-card {
  margin-bottom: 20px;
  transition: all 0.3s;
  cursor: pointer;
}

.channel-card:hover {
  border-color: #409EFF;
}

.channel-card.disabled {
  opacity: 0.6;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.channel-name {
  font-weight: 600;
  font-size: 16px;
}

.channel-info {
  color: #606266;
}

.channel-info p {
  margin: 8px 0;
}

.channel-info .el-tag {
  margin-right: 8px;
}

.status {
  margin-top: 12px;
}

.hint {
  font-size: 12px;
  color: #c0c4cc;
  margin-top: 8px !important;
}

.option-row {
  display: flex;
  align-items: center;
  margin-bottom: 8px;
}
</style>
