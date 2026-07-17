import { defineStore } from 'pinia'
import { APIError, registerUnauthorizedHandler, request, type UserView } from '../api/client'
type BootstrapStatus = 'idle' | 'loading' | 'ready'
export const useSessionStore = defineStore('session', { state: () => ({ user: null as UserView | null, bootstrapStatus: 'idle' as BootstrapStatus, bootstrapPromise: null as Promise<void> | null }), actions: {
  setUser(user: UserView | null) { this.user=user; this.bootstrapStatus='ready' },
  clear() { this.user=null; this.bootstrapStatus='ready' },
  async bootstrap() { if(this.bootstrapStatus==='ready') return; if(this.bootstrapPromise) return this.bootstrapPromise; this.bootstrapStatus='loading'; this.bootstrapPromise=request<UserView>('/auth/me').then((user)=>{this.user=user;this.bootstrapStatus='ready'}).catch((error:unknown)=>{if(error instanceof APIError&&error.status===401){this.user=null;this.bootstrapStatus='ready';return} this.bootstrapStatus='idle';throw error}).finally(()=>{this.bootstrapPromise=null}); return this.bootstrapPromise },
  async refresh() { this.bootstrapStatus='idle'; await this.bootstrap() },
  async logout() { try { await request('/auth/logout', { method: 'POST' }) } finally { this.clear() } },
} })
export function bindSessionUnauthorizedHandler(session: ReturnType<typeof useSessionStore>): void { registerUnauthorizedHandler(()=>session.clear()) }