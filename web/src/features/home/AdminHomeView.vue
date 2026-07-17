<template>
  <section class="home" aria-labelledby="admin-home-title">
    <p class="eyebrow">教师工作台</p><h1 id="admin-home-title">今天，从一道好题开始</h1>
    <p class="lead">集中查看学生学习动态，并为高中数学、物理辅导做好准备。</p>
    <div class="cards"><article><span>待关注学生</span><strong>0</strong><p>学生管理将在下一阶段开放。</p></article><article><span>本周学习提醒</span><strong>0</strong><p>及时关注需要帮助的同学。</p></article></div>
  </section>
</template>
<style scoped>.home{max-width:960px}.eyebrow{color:#1673b9;font-weight:700;letter-spacing:.06em}.home h1{margin:.45rem 0;font-size:clamp(1.8rem,4vw,2.7rem)}.lead{color:#58667c;line-height:1.7}.cards{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:18px;margin-top:32px}.cards article{padding:24px;border:1px solid #dbe4f0;border-radius:14px;background:#fff}.cards span,.cards p{color:#64738a}.cards strong{display:block;margin:10px 0;font-size:2rem;color:#183b67}@media(max-width:620px){.cards{grid-template-columns:1fr}}</style>