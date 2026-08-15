<template>
  <el-container class="app-container">
    <el-aside width="200px">
      <div class="logo">
        <h2>News Platform</h2>
      </div>
      <el-menu
        :default-active="activeMenu"
        router
        background-color="#304156"
        text-color="#bfcbd9"
        active-text-color="#409EFF"
      >
        <el-menu-item index="/dashboard">
          <el-icon><Odometer /></el-icon>
          <span>仪表盘</span>
        </el-menu-item>
        <el-menu-item index="/sources">
          <el-icon><DataLine /></el-icon>
          <span>数据源</span>
        </el-menu-item>
        <el-menu-item index="/channels">
          <el-icon><Connection /></el-icon>
          <span>渠道</span>
        </el-menu-item>
        <el-menu-item index="/health">
          <el-icon><FirstAidKit /></el-icon>
          <span>健康</span>
        </el-menu-item>
        <el-menu-item index="/messages">
          <el-icon><ChatLineSquare /></el-icon>
          <span>消息</span>
        </el-menu-item>
        <el-menu-item index="/reports">
          <el-icon><Document /></el-icon>
          <span>报告</span>
        </el-menu-item>
        <el-menu-item index="/macro">
          <el-icon><TrendCharts /></el-icon>
          <span>宏观</span>
        </el-menu-item>
        <el-menu-item index="/llm">
          <el-icon><Cpu /></el-icon>
          <span>LLM</span>
        </el-menu-item>
        <el-menu-item index="/keys">
          <el-icon><Key /></el-icon>
          <span>密钥</span>
        </el-menu-item>
        <el-menu-item index="/settings">
          <el-icon><Setting /></el-icon>
          <span>设置</span>
        </el-menu-item>
      </el-menu>
    </el-aside>
    <el-main>
      <router-view />
    </el-main>
  </el-container>
</template>

<script setup>
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()
const activeMenu = computed(() => route.path)
</script>

<style>
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

html, body, #app {
  height: 100%;
}

.app-container {
  height: 100%;
}

.el-aside {
  background-color: #304156;
}

.logo {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  border-bottom: 1px solid #3d4a5c;
}

.logo h2 {
  font-size: 18px;
  font-weight: 600;
}

.el-menu {
  border-right: none;
}

.el-main {
  background-color: #f0f2f5;
  padding: 20px;
}
</style>
