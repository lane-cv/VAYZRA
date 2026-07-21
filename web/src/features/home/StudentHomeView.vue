<script setup lang="ts">
import { onBeforeMount, onBeforeUnmount, ref } from 'vue'
import { recentLessons } from '../learning/api'
import type { RecentLesson } from '../learning/types'
const recent = ref<RecentLesson[]>([])
let controller: AbortController | undefined
onBeforeMount(async()=>{controller=new AbortController();try{recent.value=await recentLessons(controller.signal)}catch{/* Home remains useful when recent lessons are unavailable. */}})
onBeforeUnmount(()=>controller?.abort())
</script>
<template>
  <section class="home" aria-labelledby="student-home-title">
    <p class="eyebrow">学习首页</p><h1 id="student-home-title">把每一步想清楚</h1>
    <p class="lead">这里将陪你梳理高中数学和物理中的关键问题，让练习更有方向。</p>
    <article class="next-step"><span>今日学习提示</span><h2>从错题的第一步重新推导</h2><p>保持好奇，认真写下每一个已知条件和推理过程。</p><a class="start" href="/student/learning">开始学习课程</a></article>
    <section v-if="recent.length" class="recent" aria-labelledby="recent-title"><h2 id="recent-title">最近学习</h2><a v-for="item in recent" :key="item.revisionId" :href="`/student/learning/${encodeURIComponent(item.lessonId)}`"><strong>{{ item.title }}</strong><span>阅读到 {{ Math.round(item.position.scrollRatio*100) }}%</span></a></section>
  </section>
</template>
<style scoped>.home{max-width:800px}.eyebrow{color:#1673b9;font-weight:700;letter-spacing:.06em}.home h1{margin:.45rem 0;font-size:clamp(1.8rem,4vw,2.7rem)}.lead{color:#58667c;line-height:1.7}.next-step{margin-top:32px;padding:26px;border:1px solid #dbe4f0;border-radius:14px;background:linear-gradient(135deg,#fff,#eef8ff)}.next-step span{color:#1673b9;font-weight:700}.next-step h2{margin:12px 0}.next-step p{color:#58667c;line-height:1.7}.start{display:inline-block;margin-top:10px;padding:10px 15px;border-radius:8px;background:#176faf;color:#fff;text-decoration:none;font-weight:700}.recent{display:grid;gap:10px;margin-top:28px}.recent a{display:flex;justify-content:space-between;gap:16px;padding:14px;border:1px solid #dbe4f0;border-radius:10px;background:#fff;color:#176faf;text-decoration:none}.recent span{color:#58667c}</style>
