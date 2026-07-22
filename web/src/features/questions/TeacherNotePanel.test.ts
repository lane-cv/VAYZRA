import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
const api = vi.hoisted(() => ({ add: vi.fn() }))
vi.mock('./adminApi', async (original) => ({ ...(await original<typeof import('./adminApi')>()), addTeacherNote: api.add }))
import TeacherNotePanel from './TeacherNotePanel.vue'
describe('TeacherNotePanel', () => {
  it('uses an independent form with the exact privacy label', async () => {
    api.add.mockResolvedValue({id:'n2',authorUserId:'a1',body:'new private note',createdAt:'now'})
    const wrapper=mount(TeacherNotePanel,{props:{questionId:'q1',notes:[{id:'n1',authorUserId:'a1',body:'old private note',createdAt:'then'}]}})
    expect(wrapper.text()).toContain('仅老师可见'); await wrapper.get('textarea').setValue('new private note'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.emitted('added')?.[0]?.[0]).toMatchObject({body:'new private note'})
  })
  it('aborts and ignores a late note response after the selected route changes',async()=>{let resolve!:(value:unknown)=>void;api.add.mockImplementation(()=>new Promise(done=>{resolve=done}));const wrapper=mount(TeacherNotePanel,{props:{questionId:'q1',notes:[]}});await wrapper.get('textarea').setValue('old note');await wrapper.get('form').trigger('submit');const call=api.add.mock.calls[api.add.mock.calls.length-1] as unknown[];const signal=call[2] as AbortSignal;await wrapper.setProps({questionId:'q2'});expect(signal.aborted).toBe(true);resolve({id:'n1',authorUserId:'a1',body:'old note',createdAt:'now'});await flushPromises();expect(wrapper.emitted('added')).toBeUndefined()})
})
