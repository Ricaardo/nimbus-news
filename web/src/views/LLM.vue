<template>
  <div class="page-container">
    <div class="page-header">
      <h1>LLM 配置</h1>
      <el-button @click="loadConfig" :loading="loading">
        <el-icon><Refresh /></el-icon>
        刷新
      </el-button>
    </div>

    <el-row :gutter="20">
      <el-col :span="14">
        <el-card>
          <template #header>
            <span>当前配置</span>
          </template>
          <el-form :model="form" label-width="120px">
            <el-form-item label="Provider">
              <el-select v-model="form.provider">
                <el-option label="MiniMax" value="minimax" />
                <el-option label="DeepSeek" value="deepseek" />
                <el-option label="OpenAI" value="openai" />
              </el-select>
            </el-form-item>
            <el-form-item label="API URL">
              <el-input v-model="form.api_url" placeholder="https://api.minimax.com/anthropic" />
            </el-form-item>
            <el-form-item label="API Key">
              <el-input v-model="form.api_key" type="password" show-password placeholder="API Key" />
            </el-form-item>
            <el-form-item label="主模型 (Chat)">
              <el-input v-model="form.model" placeholder="MiniMax-M2.5 / deepseek-v4-flash" />
            </el-form-item>
            <el-form-item label="推理模型 (Think)">
              <el-input v-model="form.heavy_model" placeholder="MiniMax-M2.5 / deepseek-v4-pro" />
            </el-form-item>
            <el-form-item label="推理模式">
              <el-select v-model="form.thinking_mode">
                <el-option label="thinking" value="thinking" />
                <el-option label="thinking_max" value="thinking_max" />
              </el-select>
            </el-form-item>
            <el-form-item>
              <el-button type="primary" @click="saveConfig" :loading="saving">保存</el-button>
              <el-button @click="testConnection" :loading="testing">测试连接</el-button>
            </el-form-item>
          </el-form>
        </el-card>
      </el-col>

      <el-col :span="10">
        <el-card>
          <template #header>
            <span>快捷切换</span>
          </template>
          <div class="preset-section">
            <el-card shadow="hover" class="preset-card" :class="{ active: form.provider === 'minimax' }">
              <div class="preset-header">
                <span class="preset-name">MiniMax M2.5</span>
                <el-button size="small" type="primary" @click="applyPreset('minimax')">应用</el-button>
              </div>
              <div class="preset-detail">API: api.minimax.com/anthropic</div>
            </el-card>
            <el-card shadow="hover" class="preset-card" :class="{ active: form.provider === 'deepseek' }">
              <div class="preset-header">
                <span class="preset-name">DeepSeek v4</span>
                <el-button size="small" type="primary" @click="applyPreset('deepseek')">应用</el-button>
              </div>
              <div class="preset-detail">Flash + Pro reasoning models</div>
            </el-card>
          </div>

          <el-divider />

          <div v-if="testResult !== null" class="test-result">
            <el-alert
              :title="testResult.ok ? '连接成功' : '连接失败'"
              :type="testResult.ok ? 'success' : 'error'"
              :description="testResult.ok ? testResult.response : testResult.error"
              :closable="false"
            />
          </div>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { getLLMConfig, updateLLMConfig, testLLMConnection } from '../api/config'

const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const testResult = ref(null)

const form = reactive({
  provider: 'minimax',
  api_url: '',
  api_key: '',
  model: '',
  heavy_model: '',
  thinking_mode: 'thinking',
})

const presets = {
  minimax: {
    provider: 'minimax',
    api_url: 'https://api.minimax.com/anthropic',
    model: 'MiniMax-M2.5',
    heavy_model: 'MiniMax-M2.5',
    thinking_mode: 'thinking',
  },
  deepseek: {
    provider: 'deepseek',
    api_url: 'https://api.deepseek.com',
    model: 'deepseek-v4-flash',
    heavy_model: 'deepseek-v4-pro',
    thinking_mode: 'thinking',
  },
  openai: {
    provider: 'openai',
    api_url: 'https://api.openai.com/v1',
    model: 'gpt-4o',
    heavy_model: 'gpt-4o',
    thinking_mode: 'thinking',
  },
}

const loadConfig = async () => {
  loading.value = true
  try {
    const res = await getLLMConfig()
    const cfg = res.data.llm
    Object.assign(form, {
      provider: cfg.provider || 'minimax',
      api_url: cfg.api_url || '',
      api_key: cfg.api_key || '',
      model: cfg.model || '',
      heavy_model: cfg.heavy_model || '',
      thinking_mode: cfg.thinking_mode || 'thinking',
    })
  } catch (err) {
    ElMessage.error('加载失败: ' + err.message)
  } finally {
    loading.value = false
  }
}

const saveConfig = async () => {
  saving.value = true
  try {
    await updateLLMConfig({ ...form })
    ElMessage.success('LLM 配置已保存')
  } catch (err) {
    ElMessage.error('保存失败: ' + err.message)
  } finally {
    saving.value = false
  }
}

const testConnection = async () => {
  if (!form.api_key || form.api_key.includes('***')) {
    ElMessage.warning('请先输入完整的 API Key')
    return
  }
  testing.value = true
  testResult.value = null
  try {
    const res = await testLLMConnection({
      provider: form.provider,
      api_url: form.api_url,
      api_key: form.api_key,
      model: form.model,
    })
    testResult.value = res.data
  } catch (err) {
    testResult.value = { ok: false, error: err.message }
  } finally {
    testing.value = false
  }
}

const applyPreset = (name) => {
  const p = presets[name]
  Object.assign(form, { ...p, api_key: form.api_key })
}

onMounted(loadConfig)
</script>

<style scoped>
.page-container { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.page-header h1 { font-size: 24px; font-weight: 600; color: #303133; }
.preset-section { display: flex; flex-direction: column; gap: 12px; }
.preset-card { cursor: pointer; }
.preset-card.active { border-color: #409EFF; background: #ecf5ff; }
.preset-header { display: flex; justify-content: space-between; align-items: center; }
.preset-name { font-weight: 600; }
.preset-detail { font-size: 12px; color: #909399; margin-top: 4px; }
.test-result { margin-top: 12px; }
</style>
