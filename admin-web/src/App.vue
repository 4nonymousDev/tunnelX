<template>
  <main v-if="api.restoringSession.value || !api.authenticated.value" class="login-shell">
    <div v-if="api.restoringSession.value" class="login-card" role="status"><div class="brand-mark">TX</div><h1>TunnelX</h1><p class="login-copy">正在恢复登录…</p></div>
    <form v-else class="login-card" @submit.prevent="login">
      <div class="brand-mark">TX</div><p class="eyebrow">服务端管理后台</p><h1>TunnelX</h1><p class="login-copy">使用管理员账号登录。刷新页面会保持登录状态。</p>
      <label class="login-label">管理员账号<input v-model="usernameInput" name="username" autocomplete="username" autocapitalize="none" spellcheck="false" autofocus maxlength="64" required :disabled="api.authBusy.value" /></label>
      <label class="login-label">密码<input v-model="passwordInput" name="password" autocomplete="current-password" type="password" required :disabled="api.authBusy.value" /></label>
      <p v-if="api.error.value" class="error-banner" role="alert">{{ api.error.value }}</p><button class="button primary login-button" type="submit" :disabled="api.authBusy.value || !usernameInput.trim() || !passwordInput">{{ api.authBusy.value ? '登录中…' : '进入管理后台' }}</button>
      <p class="session-copy">登录最长保留 7 天，闲置 12 小时后需重新登录。公用电脑使用后请退出。</p>
      <details class="setup-help"><summary>首次使用或没有管理员账号</summary><p>请在服务器上先停止服务，再使用原数据目录创建管理员：</p><code>tunnel-server -data-dir &lt;原数据目录&gt; -admin-account &lt;管理员账号&gt;</code><p>命令会安全地提示输入密码，完成后重新启动服务。普通客户端账号需要管理员授权后才能登录后台。</p></details>
    </form>
  </main>
  <div v-else class="app-shell">
    <aside class="sidebar"><div class="brand"><span class="brand-mark small">TX</span><div><strong>TunnelX</strong><small>Server Console</small></div></div><nav class="nav"><button v-for="item in navItems" :key="item.id" type="button" :class="{ active: page === item.id }" @click="navigate(item.id)">{{ item.label }}</button></nav><div class="sidebar-footer"><span class="connection"><i :class="{ connected: api.eventsConnected.value }"></i>{{ api.eventsConnected.value ? '实时更新已连接' : '实时更新重连中' }}</span><span class="current-user">{{ api.username.value }}</span><button class="logout" type="button" :disabled="api.authBusy.value" @click="logout">{{ api.authBusy.value ? '退出中…' : '退出登录' }}</button></div></aside>
    <section class="content"><div v-if="api.error.value" class="error-banner global">{{ api.error.value }}</div><OverviewView v-if="page === 'overview'" :api="api" /><AccountsView v-else-if="page === 'accounts'" :api="api" /><SessionsView v-else-if="page === 'sessions'" :api="api" /><ClientsView v-else-if="page === 'clients'" :api="api" @select="showClient" @accounts="navigate('accounts')" /><AuditView v-else-if="page === 'audit'" :api="api" /><ClientDetailView v-else-if="page === 'client-detail' && selectedFingerprint" :api="api" :fingerprint="selectedFingerprint" @back="navigate('clients')" /></section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, shallowRef, watch } from 'vue'
import { useAdminApi } from './composables/useAdminApi'
import AuditView from './views/AuditView.vue'
import AccountsView from './views/AccountsView.vue'
import ClientDetailView from './views/ClientDetailView.vue'
import ClientsView from './views/ClientsView.vue'
import OverviewView from './views/OverviewView.vue'
import SessionsView from './views/SessionsView.vue'

type Page = 'overview' | 'accounts' | 'sessions' | 'clients' | 'audit' | 'client-detail'
const api = useAdminApi()
const usernameInput = shallowRef('')
const passwordInput = shallowRef('')
const page = shallowRef<Page>('overview')
const selectedFingerprint = shallowRef('')
const navItems: Array<{ id: Exclude<Page, 'client-detail'>; label: string }> = [{ id: 'overview', label: '概览' }, { id: 'accounts', label: '账号管理' }, { id: 'sessions', label: '在线会话' }, { id: 'clients', label: '设备与高级管理' }, { id: 'audit', label: '审计日志' }]
onMounted(() => { void api.restoreSession() })
onUnmounted(() => { passwordInput.value = '' })
watch(api.authenticated, value => { if (!value) { page.value = 'overview'; selectedFingerprint.value = ''; passwordInput.value = '' } })
async function login() {
  if (api.authBusy.value) return
  const password = passwordInput.value
  passwordInput.value = ''
  try { await api.authenticate(usernameInput.value, password) } catch { /* error is exposed by composable */ }
}
function navigate(next: Exclude<Page, 'client-detail'>) { page.value = next; if (next !== 'clients') selectedFingerprint.value = '' }
function showClient(fingerprint: string) { selectedFingerprint.value = fingerprint; page.value = 'client-detail' }
async function logout() { try { await api.logout(); passwordInput.value = '' } catch { /* Keep the session visible until the server confirms logout. */ } }
</script>

<style scoped>
.session-copy,.setup-help { color: var(--muted); font-size: .78rem; line-height: 1.6; }.setup-help { margin-top: 1rem; }.setup-help summary { cursor: pointer; }.setup-help code { display: block; white-space: normal; overflow-wrap: anywhere; padding: .65rem; background: var(--panel-soft); }.current-user { color: var(--text); font-size: .82rem; overflow-wrap: anywhere; }
.login-shell { min-height: 100vh; display: grid; place-items: center; padding: 1.5rem; background: radial-gradient(circle at 20% 20%, #153b41 0, transparent 32%), #071013; }.login-card { width: min(27rem, 100%); padding: 2rem; border: 1px solid var(--border); border-radius: 18px; background: rgba(13, 29, 33, .94); box-shadow: 0 2rem 5rem #0008; }.login-card h1 { margin: .4rem 0; font-size: 2.2rem; }.login-copy { color: var(--muted); line-height: 1.6; }.login-label { display: grid; gap: .5rem; margin-top: 1.5rem; color: var(--muted); font-size: .82rem; }.login-button { width: 100%; margin-top: 1rem; }.brand-mark { display: grid; width: 3.1rem; height: 3.1rem; place-items: center; border-radius: 12px; background: var(--accent); color: #041315; font-weight: 900; letter-spacing: -.08em; }.brand-mark.small { width: 2.3rem; height: 2.3rem; border-radius: 9px; font-size: .78rem; }.app-shell { min-height: 100vh; display: grid; grid-template-columns: 15rem minmax(0, 1fr); }.sidebar { position: sticky; top: 0; height: 100vh; display: flex; flex-direction: column; padding: 1.2rem; border-right: 1px solid var(--border); background: #091316; }.brand { display: flex; align-items: center; gap: .7rem; margin-bottom: 2rem; }.brand small { display: block; color: var(--muted); font-size: .68rem; letter-spacing: .08em; text-transform: uppercase; }.nav { display: grid; gap: .35rem; }.nav button { padding: .72rem .8rem; border: 0; border-radius: 8px; background: transparent; color: var(--muted); text-align: left; cursor: pointer; }.nav button:hover, .nav button.active { background: var(--panel-soft); color: var(--text); }.nav button.active { box-shadow: inset 3px 0 var(--accent); }.sidebar-footer { display: grid; gap: .8rem; margin-top: auto; }.connection { color: var(--muted); font-size: .74rem; }.connection i { display: inline-block; width: .5rem; height: .5rem; margin-right: .45rem; border-radius: 50%; background: #b36b50; }.connection i.connected { background: #50b393; box-shadow: 0 0 9px #50b393; }.logout { padding: 0; border: 0; background: transparent; color: var(--muted); text-align: left; font-size: .75rem; cursor: pointer; }.content { min-width: 0; padding: 2rem clamp(1rem, 4vw, 4rem); }.global { margin-bottom: 1rem; }
@media (max-width: 720px) { .app-shell { grid-template-columns: 1fr; }.sidebar { position: static; height: auto; }.nav { grid-template-columns: repeat(4, 1fr); }.nav button { padding: .6rem .25rem; text-align: center; font-size: .78rem; }.sidebar-footer { display: flex; flex-wrap: wrap; align-items: center; margin-top: 1rem; }.content { padding-top: 1.2rem; } }
</style>
