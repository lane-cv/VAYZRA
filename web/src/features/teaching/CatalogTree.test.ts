import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import CatalogTree from './CatalogTree.vue'
import type { CatalogNode } from './types'

const nodes: CatalogNode[] = [
  { id: 'grade-1', parentId: '', kind: 'grade', name: '高一', description: '', sortKey: 10, status: 'active', published: false },
  { id: 'term-1', parentId: 'grade-1', kind: 'term', name: '上学期', description: '', sortKey: 10, status: 'active', published: false },
]

describe('CatalogTree', () => {
  it('renders semantic tree items and selects with the keyboard', async () => {
    const wrapper = mount(CatalogTree, { props: { nodes, selectedId: '' } })
    expect(wrapper.get('[role="tree"]').attributes('aria-label')).toBe('教学目录')
    const grade = wrapper.get('[role="treeitem"][aria-label="高一"]')
    await grade.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('select')?.[0]).toEqual([nodes[0]])
  })

  it('offers button alternatives for ordering and explicit archive actions', async () => {
    const wrapper = mount(CatalogTree, { props: { nodes, selectedId: 'grade-1' } })
    await wrapper.get('button[aria-label="上移 高一"]').trigger('click')
    await wrapper.get('button[aria-label="下移 高一"]').trigger('click')
    await wrapper.get('button[aria-label="归档 高一"]').trigger('click')
    expect(wrapper.emitted('reorder')).toEqual([[nodes[0], -1], [nodes[0], 1]])
    expect(wrapper.emitted('archive')?.[0]).toEqual([nodes[0]])
  })

  it('does not use array indexes as Vue keys', () => {
    const wrapper = mount(CatalogTree, { props: { nodes, selectedId: '' } })
    expect(wrapper.findAll('[data-node-id]').map((item) => item.attributes('data-node-id'))).toEqual(['grade-1', 'term-1'])
  })
})
