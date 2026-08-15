import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/',
    redirect: '/dashboard'
  },
  {
    path: '/dashboard',
    name: 'Dashboard',
    component: () => import('./views/Dashboard.vue')
  },
  {
    path: '/sources',
    name: 'Sources',
    component: () => import('./views/Sources.vue')
  },
  {
    path: '/channels',
    name: 'Channels',
    component: () => import('./views/Channels.vue')
  },
  {
    path: '/health',
    name: 'Health',
    component: () => import('./views/Health.vue')
  },
  {
    path: '/messages',
    name: 'Messages',
    component: () => import('./views/Messages.vue')
  },
  {
    path: '/reports',
    name: 'Reports',
    component: () => import('./views/Reports.vue')
  },
  {
    path: '/macro',
    name: 'Macro',
    component: () => import('./views/Macro.vue')
  },
  {
    path: '/llm',
    name: 'LLM',
    component: () => import('./views/LLM.vue')
  },
  {
    path: '/keys',
    name: 'Keys',
    component: () => import('./views/Keys.vue')
  },
  {
    path: '/settings',
    name: 'Settings',
    component: () => import('./views/Settings.vue')
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

export default router
