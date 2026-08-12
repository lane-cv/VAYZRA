<template><RouterView /></template>

<style>
:root {
  font-family: Inter, ui-sans-serif, "PingFang SC", "Microsoft YaHei", system-ui, sans-serif;
  color: #0f172a;
  background: #f1f5f9;
  font-synthesis: none;
  text-rendering: optimizeLegibility;
  --hl-primary: #0f766e;
  --hl-primary-strong: #115e59;
  --hl-primary-soft: #ccfbf1;
  --hl-on-primary: #ffffff;
  --hl-accent: #14b8a6;
  --hl-focus-ring: #0f766e;
  --hl-bg: #f1f5f9;
  --hl-bg-deep: #e2e8f0;
  --hl-surface: rgba(255, 255, 255, .88);
  --hl-surface-solid: #fff;
  --hl-surface-muted: #f8fafc;
  --hl-text: #0f172a;
  --hl-text-muted: #64748b;
  --hl-text-soft: #94a3b8;
  --hl-border: #e2e8f0;
  --hl-border-strong: #cbd5e1;
  --hl-success: #15803d;
  --hl-success-soft: #f0fdf4;
  --hl-warning: #a16207;
  --hl-warning-soft: #fffbeb;
  --hl-danger: #dc2626;
  --hl-danger-soft: #fef2f2;
  --hl-shadow: 0 18px 48px rgba(15, 23, 42, .08);
  --hl-shadow-soft: 0 8px 24px rgba(15, 23, 42, .06);
}

:root[data-theme="dark"] {
  color: #e2e8f0;
  background: #020617;
  --hl-primary: #0f766e;
  --hl-primary-strong: #5eead4;
  --hl-primary-soft: rgba(20, 184, 166, .14);
  --hl-on-primary: #ffffff;
  --hl-accent: #2dd4bf;
  --hl-focus-ring: #5eead4;
  --hl-bg: #020617;
  --hl-bg-deep: #0f172a;
  --hl-surface: rgba(15, 23, 42, .88);
  --hl-surface-solid: #0f172a;
  --hl-surface-muted: #111c31;
  --hl-text: #e2e8f0;
  --hl-text-muted: #94a3b8;
  --hl-text-soft: #64748b;
  --hl-border: #1e293b;
  --hl-border-strong: #334155;
  --hl-success: #4ade80;
  --hl-success-soft: rgba(20, 83, 45, .26);
  --hl-warning: #fbbf24;
  --hl-warning-soft: rgba(120, 53, 15, .28);
  --hl-danger: #f87171;
  --hl-danger-soft: rgba(127, 29, 29, .18);
  --hl-shadow: 0 20px 54px rgba(0, 0, 0, .28);
  --hl-shadow-soft: 0 10px 28px rgba(0, 0, 0, .2);
}

* { box-sizing: border-box; }
html { min-width: 320px; background: var(--hl-bg); }
body { margin: 0; min-width: 320px; min-height: 100vh; background: var(--hl-bg); color: var(--hl-text); }
button, input { font: inherit; }
button, a { -webkit-tap-highlight-color: transparent; }
button:focus-visible,
input:focus-visible,
textarea:focus-visible,
select:focus-visible,
summary:focus-visible,
a[href]:focus-visible,
[tabindex]:not([tabindex="-1"]):focus-visible {
  outline: 3px solid var(--hl-focus-ring) !important;
  outline-offset: 2px !important;
}
::selection { background: var(--hl-primary-soft); color: var(--hl-text); }
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { scroll-behavior: auto !important; transition-duration: .01ms !important; animation-duration: .01ms !important; animation-iteration-count: 1 !important; }
}
</style>
