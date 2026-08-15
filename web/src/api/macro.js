import axios from 'axios'

const api = axios.create({
  baseURL: '/api',
  timeout: 30000
})

// 美股宏观:关键指标概览(最新值/前值/环比)+ 未来发布日历
export const getMacroOverview = () => api.get('/macro/overview')
// 单序列时间序列(range: 6m/1y/3y/5y)
export const getMacroSeries = (series, range) =>
  api.get(`/macro/series?series=${series}&range=${range}`)
