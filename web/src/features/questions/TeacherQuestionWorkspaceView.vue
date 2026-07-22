<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import TeacherQuestionListView from './TeacherQuestionListView.vue'
const route=useRoute();const selected=computed(()=>typeof route.params.questionId==='string'&&route.params.questionId.length>0)
</script>
<template><div class="teacher-workspace" :class="{'has-selection':selected}"><TeacherQuestionListView class="list-pane"/><section class="detail-pane" aria-label="问题详情"><RouterView/></section></div></template>
<style scoped>.teacher-workspace{display:grid;grid-template-columns:minmax(320px,40%) minmax(0,1fr);gap:24px;align-items:start}.list-pane{max-height:calc(100vh - 120px);overflow:auto}.detail-pane{min-width:0}@media(max-width:900px){.teacher-workspace{display:block}.has-selection .list-pane{display:none}.teacher-workspace:not(.has-selection) .detail-pane{display:none}}</style>
