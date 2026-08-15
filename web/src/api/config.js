import axios from 'axios'

const api = axios.create({
  baseURL: '/api',
  timeout: 10000
})

// 系统状态
export const getStatus = () => api.get('/status')

// 完整配置
export const getConfig = () => api.get('/config')
export const updateConfig = (config) => api.put('/config', config)
export const reloadConfig = () => api.post('/config/reload')

// 渠道管理
export const getChannels = () => api.get('/channels')
export const createChannel = (data) => api.post('/channels', data)
export const getChannel = (name) => api.get(`/channels/${name}`)
export const updateChannel = (name, data) => api.put(`/channels/${name}`, data)
export const toggleChannel = (name, enabled) => api.post(`/channels/${name}/toggle`, { enabled })

// 数据源管理
export const getSources = () => api.get('/sources')
export const getSource = (name) => api.get(`/sources/${name}`)
export const getSourceTypes = () => api.get('/sources/types')
export const createSource = (data) => api.post('/sources', data)
export const updateSource = (name, data) => api.put(`/sources/${name}`, data)
export const deleteSource = (name) => api.delete(`/sources/${name}`)
export const toggleSource = (name, enabled) => api.post(`/sources/${name}/toggle`, { enabled })

// 源健康监控
export const getSourcesHealth = (details) => api.get('/sources/health' + (details ? '?details=true' : ''))
export const getSourceHealth = (name) => api.get(`/sources/${name}/health`)
export const resetSourceHealth = (name) => api.post(`/sources/${name}/health/reset`)

// 调试
export const getDebugMessages = () => api.get('/debug/messages')

// 聚合报告历史
export const getReports = (type, limit = 20) => {
  const params = new URLSearchParams()
  if (type) params.set('type', type)
  if (limit) params.set('limit', String(limit))
  return api.get('/reports?' + params.toString())
}

// LLM 管理
export const getLLMConfig = () => api.get('/llm')
export const updateLLMConfig = (config) => api.put('/llm', config)
export const testLLMConnection = (config) => api.post('/llm/test', config)

// Discord
export const getDiscordStatus = () => api.get('/discord/status')
export const getDiscordThreads = () => api.get('/discord/threads')
export const cleanupDiscordThreads = () => api.post('/discord/threads/cleanup')

// API 密钥管理
export const getKeys = () => api.get('/keys')
export const updateKey = (id, value) => api.put('/keys', { id, value })

// 子系统配置
export const getFiltersConfig = () => api.get('/config/filters')
export const updateFiltersConfig = (data) => api.put('/config/filters', data)
export const getAlertConfig = () => api.get('/config/alert')
export const updateAlertConfig = (data) => api.put('/config/alert', data)
export const getHealthConfig = () => api.get('/config/health')
export const updateHealthConfig = (data) => api.put('/config/health', data)

export default api

// 主动拉取:同步执行源 Fetch 并返回消息(不推送)
export const fetchSourceNow = (name) => api.post(`/sources/${name}/fetch`)
