<script setup lang="ts">
import type { CatalogNode } from './types'

defineProps<{ nodes: CatalogNode[]; selectedId: string }>()
const emit = defineEmits<{
  select: [node: CatalogNode]
  reorder: [node: CatalogNode, direction: -1 | 1]
  archive: [node: CatalogNode]
  rename: [node: CatalogNode]
}>()

const kindLabel: Record<CatalogNode['kind'], string> = { grade: '年级', term: '学期', subject: '学科', chapter: '章节', lesson: '课程' }
function level(node: CatalogNode) { return ({ grade: 1, term: 2, subject: 3, chapter: 4, lesson: 5 })[node.kind] }
</script>

<template>
  <ul class="catalog-tree" role="tree" aria-label="教学目录">
    <li v-for="node in nodes" :key="node.id" :data-node-id="node.id" class="tree-row">
      <button
        class="node-button"
        :class="{ selected: selectedId === node.id }"
        role="treeitem"
        :aria-label="node.name"
        :aria-level="level(node)"
        :aria-selected="selectedId === node.id"
        :style="{ paddingInlineStart: `${12 + (level(node) - 1) * 18}px` }"
        @click="emit('select', node)"
        @keydown.enter.prevent="emit('select', node)"
        @keydown.space.prevent="emit('select', node)"
      >
        <span class="kind">{{ kindLabel[node.kind] }}</span><span>{{ node.name }}</span>
      </button>
      <div class="row-actions">
        <button type="button" :aria-label="`重命名 ${node.name}`" @click="emit('rename', node)">改名</button>
        <button type="button" :aria-label="`上移 ${node.name}`" @click="emit('reorder', node, -1)">上移</button>
        <button type="button" :aria-label="`下移 ${node.name}`" @click="emit('reorder', node, 1)">下移</button>
        <button type="button" :aria-label="`归档 ${node.name}`" @click="emit('archive', node)">归档</button>
      </div>
    </li>
  </ul>
</template>

<style scoped>
.catalog-tree{display:grid;gap:8px;padding:0;list-style:none}.tree-row{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:8px;align-items:center;border:1px solid var(--hl-border);border-radius:10px;background:var(--hl-surface-solid)}.node-button{display:flex;gap:10px;align-items:center;min-height:48px;border:0;border-radius:10px;background:transparent;color:var(--hl-text);text-align:left;cursor:pointer}.node-button.selected{outline:2px solid var(--hl-primary);background:var(--hl-primary-soft)}.kind{min-width:38px;color:var(--hl-text-muted);font-size:.78rem}.row-actions{display:flex;gap:4px;padding-right:8px}.row-actions button{border:0;background:transparent;color:var(--hl-primary-strong);cursor:pointer}@media(max-width:700px){.tree-row{grid-template-columns:1fr}.row-actions{padding:0 10px 10px;justify-content:flex-end}}
</style>
