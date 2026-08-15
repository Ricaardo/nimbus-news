<template>
  <div class="page-container">
    <div class="page-header">
      <h1>API 密钥管理</h1>
      <el-button @click="refreshData" :loading="loading">
        <el-icon><Refresh /></el-icon>
        刷新
      </el-button>
    </div>

    <el-table :data="keys" v-loading="loading" stripe>
      <el-table-column prop="name" label="名称" width="220" />
      <el-table-column prop="category" label="分类" width="100">
        <template #default="{ row }">
          <el-tag :type="categoryType(row.category)" size="small">{{ row.category }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="value" label="当前值">
        <template #default="{ row }">
          <code class="key-value">{{ row.value || '(未配置)' }}</code>
        </template>
      </el-table-column>
      <el-table-column prop="enabled" label="启用" width="80">
        <template #default="{ row }">
          <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
            {{ row.enabled ? '已启用' : '已禁用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="100">
        <template #default="{ row }">
          <el-button link type="primary" @click="editKey(row)">编辑</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-empty v-if="keys.length === 0 && !loading" description="暂无外部 API 密钥" />

    <el-dialog v-model="dialogVisible" title="编辑密钥" width="500px">
      <el-form :model="editForm" label-width="100px">
        <el-form-item label="名称">
          <el-input :model-value="editForm.name" disabled />
        </el-form-item>
        <el-form-item label="分类">
          <el-tag :type="categoryType(editForm.category)" size="small">{{ editForm.category }}</el-tag>
        </el-form-item>
        <el-form-item label="新值">
          <el-input
            v-model="editForm.value"
            type="password"
            show-password
            placeholder="输入新的密钥值"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="saveKey" :loading="saving">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { getKeys, updateKey } from '../api/config'

const loading = ref(false)
const saving = ref(false)
const keys = ref([])
const dialogVisible = ref(false)
const editForm = ref({})

const refreshData = async () => {
  loading.value = true
  try {
    const res = await getKeys()
    keys.value = res.data.keys || []
  } catch (err) {
    ElMessage.error('获取密钥列表失败: ' + err.message)
  } finally {
    loading.value = false
  }
}

const editKey = (row) => {
  editForm.value = { ...row, value: '' }
  dialogVisible.value = true
}

const saveKey = async () => {
  if (!editForm.value.value) {
    ElMessage.error('请输入新的密钥值')
    return
  }
  saving.value = true
  try {
    await updateKey(editForm.value.id, editForm.value.value)
    ElMessage.success('密钥已更新')
    dialogVisible.value = false
    await refreshData()
  } catch (err) {
    ElMessage.error('保存失败: ' + (err.response?.data?.error || err.message))
  } finally {
    saving.value = false
  }
}

const categoryType = (cat) => ({ LLM: '', '数据源': 'warning', '渠道': 'success' }[cat] || 'info')

onMounted(refreshData)
</script>

<style scoped>
.page-container { padding: 20px; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.page-header h1 { font-size: 24px; font-weight: 600; color: #303133; }
.key-value { font-family: monospace; font-size: 13px; background: #f5f7fa; padding: 2px 8px; border-radius: 4px; }
</style>
