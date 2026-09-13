<template>
  <main v-if="!api.authenticated.value" class="login-shell">
    <form class="login-card" @submit.prevent="login">
      <div class="brand-mark">TX</div><p class="eyebrow">本机管理入口</p><h1>TunnelX</h1><p class="login-copy">输入服务端管理 Token。Token 只保存在本页面内存中，刷新后将被清除。</p>
      <label class="token-label">管理 Token<input v-model="tokenInput" autocomplete="off" autofocus type="password" placeholder="64 位十六进制 Token" /></label>
      <p v-if="api.error.value" class="error-banner">{{ api.error.value }}</p><button class="button primary login-button" type="submit" :disabled="api.loading.value || !tokenInput.trim()">{{ api.loading.value ? '验证中…' : '进入管理后台' }}</button>
    </form>
  </main>
  <div v-else class="app-shell">
    <aside class="sidebar"><div class="brand"><span class="brand-mark small">TX</span><div><strong>TunnelX</strong><small>Server Console</small></div></div><nav class="nav"><button v-for="item in navItems" :key="item.id" type="button" :class="{ active: page === item.id }" @click="navigate(item.id)">{{ item.label }}</button></nav><div class="sidebar-footer"><span class="connection"><i :class="{ connected: api.eventsConnected.value }"></i>{{ api.eventsConnected.value ? '实时更新已连接' : '实时更新重连中' }}</span><button class="logout" type="button" @click="logout">清除 Token 并退出</button></div></aside>
    <section class="content"><div v-if="api.error.value" class="error-banner global">{{ api.error.value }}</div><OverviewView v-if="page === 'overview'" :api="api" /><SessionsView v-else-if="page === 'sessions'" :api="api" /><ClientsView v-else-if="page === 'clients'" :api="api" @select="showClient" /><AuditView v-else-if="page === 'audit'" :api="api" /><ClientDetailView v-else-if="page === 'client-detail' && selectedFingerprint" :api="api" :fingerprint="selectedFingerprint" @back="navigate('clients')" /></section>
  </div>
</template>

<script setup lang="ts">
import { shallowRef } from 'vue'
import { useAdminApi } from './composables/useAdminApi'
import AuditView from './views/AuditView.vue'
import ClientDetailView from './views/ClientDetailView.vue'
import ClientsView from './views/ClientsView.vue'
import OverviewView from './views/OverviewView.vue'
import SessionsView from './views/SessionsView.vue'

type Page = 'overview' | 'sessions' | 'clients' | 'audit' | 'client-detail'
const api = useAdminApi()
const tokenInput = shallowRef('')
const page = shallowRef<Page>('overview')
const selectedFingerprint = shallowRef('')
const navItems: Array<{ id: Exclude<Page, 'client-detail'>; label: string }> = [{ id: 'overview', label: '概览' }, { id: 'sessions', label: '在线会话' }, { id: 'clients', label: '客户端管理' }, { id: 'audit', label: '审计日志' }]
async function login() { try { await api.authenticate(tokenInput.value); tokenInput.value = '' } catch { /* error is exposed by composable */ } }
function navigate(next: Exclude<Page, 'client-detail'>) { page.value = next; if (next !== 'clients') selectedFingerprint.value = '' }
function showClient(fingerprint: string) { selectedFingerprint.value = fingerprint; page.value = 'client-detail' }
function logout() { api.clearToken(); tokenInput.value = ''; page.value = 'overview'; selectedFingerprint.value = '' }
</script>

<style scoped>
.login-shell { min-height: 100vh; display: grid; place-items: center; padding: 1.5rem; background: radial-gradient(circle at 20% 20%, #153b41 0, transparent 32%), #071013; }.login-card { width: min(27rem, 100%); padding: 2rem; border: 1px solid var(--border); border-radius: 18px; background: rgba(13, 29, 33, .94); box-shadow: 0 2rem 5rem #0008; }.login-card h1 { margin: .4rem 0; font-size: 2.2rem; }.login-copy { color: var(--muted); line-height: 1.6; }.token-label { display: grid; gap: .5rem; margin-top: 1.5rem; color: var(--muted); font-size: .82rem; }.login-button { width: 100%; margin-top: 1rem; }.brand-mark { display: grid; width: 3.1rem; height: 3.1rem; place-items: center; border-radius: 12px; background: var(--accent); color: #041315; font-weight: 900; letter-spacing: -.08em; }.brand-mark.small { width: 2.3rem; height: 2.3rem; border-radius: 9px; font-size: .78rem; }.app-shell { min-height: 100vh; display: grid; grid-template-columns: 15rem minmax(0, 1fr); }.sidebar { position: sticky; top: 0; height: 100vh; display: flex; flex-direction: column; padding: 1.2rem; border-right: 1px solid var(--border); background: #091316; }.brand { display: flex; align-items: center; gap: .7rem; margin-bottom: 2rem; }.brand small { display: block; color: var(--muted); font-size: .68rem; letter-spacing: .08em; text-transform: uppercase; }.nav { display: grid; gap: .35rem; }.nav button { padding: .72rem .8rem; border: 0; border-radius: 8px; background: transparent; color: var(--muted); text-align: left; cursor: pointer; }.nav button:hover, .nav button.active { background: var(--panel-soft); color: var(--text); }.nav button.active { box-shadow: inset 3px 0 var(--accent); }.sidebar-footer { display: grid; gap: .8rem; margin-top: auto; }.connection { color: var(--muted); font-size: .74rem; }.connection i { display: inline-block; width: .5rem; height: .5rem; margin-right: .45rem; border-radius: 50%; background: #b36b50; }.connection i.connected { background: #50b393; box-shadow: 0 0 9px #50b393; }.logout { padding: 0; border: 0; background: transparent; color: var(--muted); text-align: left; font-size: .75rem; cursor: pointer; }.content { min-width: 0; padding: 2rem clamp(1rem, 4vw, 4rem); }.global { margin-bottom: 1rem; }
@media (max-width: 720px) { .app-shell { grid-template-columns: 1fr; }.sidebar { position: static; height: auto; }.nav { grid-template-columns: repeat(4, 1fr); }.nav button { padding: .6rem .25rem; text-align: center; font-size: .78rem; }.sidebar-footer { display: none; }.content { padding-top: 1.2rem; } }
</style>
