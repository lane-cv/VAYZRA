import { flushPromises,mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { createMemoryHistory,createRouter,RouterView } from 'vue-router'
import { describe,expect,it,vi } from 'vitest'
const state=vi.hoisted(()=>({mounts:0}))
vi.mock('./TeacherQuestionListView.vue',()=>({default:{data:()=>({filter:''}),mounted(){state.mounts+=1},template:'<section data-testid="persistent-list"><input v-model="filter"></section>'}}))
import TeacherQuestionWorkspaceView from './TeacherQuestionWorkspaceView.vue'
const Placeholder=defineComponent({template:'<p>placeholder</p>'}),Detail=defineComponent({template:'<p>detail</p>'})
describe('TeacherQuestionWorkspaceView',()=>{it('preserves one list instance, filters and scroll across selection routes',async()=>{state.mounts=0;const router=createRouter({history:createMemoryHistory(),routes:[{path:'/admin/questions',component:TeacherQuestionWorkspaceView,children:[{path:'',component:Placeholder},{path:':questionId',component:Detail}]}]});await router.push('/admin/questions');await router.isReady();const wrapper=mount(RouterView,{global:{plugins:[router]}});await flushPromises();const list=wrapper.get('[data-testid="persistent-list"]');await list.get('input').setValue('pending');list.element.scrollTop=37;await router.push('/admin/questions/11111111-1111-4111-8111-111111111111');await flushPromises();expect(state.mounts).toBe(1);expect(wrapper.get('[data-testid="persistent-list"]').element).toBe(list.element);expect(wrapper.get('input').element.value).toBe('pending');expect(list.element.scrollTop).toBe(37);expect(wrapper.text()).toContain('detail')})})
