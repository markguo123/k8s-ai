/* P7.3 登录页动态背景：canvas 粒子网络。来源是前端原型草稿（`prototype/login-eg.html`），
   该草稿未随仓库保留 —— 本文件即仓库内唯一实现，口径见
   docs/02-架构与设计/14-方案-前端体验优化-四期.md §8（背景不再做替换式改造）。
   仅把外层 IIFE 包成可重复调用的工厂函数（便于路由重进时重新初始化 canvas）。 */
export function initLoginNet() {

  'use strict';

  var canvas = document.getElementById('net');
  if (!canvas) return function () {};
  var scene = document.querySelector('.scene');
  var form = document.querySelector('.form');
  var card = document.querySelector('.card');
  var ctx = canvas.getContext('2d');
  var reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var rafId = 0, pulseTimer = 0, stopped = false;

  var W = 0, H = 0, DPR = 1;
  var nodes = [], pulses = [];
  var mouse = { x: -1e4, y: -1e4 };
  var LINK = 150;

  function resize() {
    DPR = Math.min(window.devicePixelRatio || 1, 2);
    W = window.innerWidth;
    H = window.innerHeight;
    canvas.width = Math.floor(W * DPR);
    canvas.height = Math.floor(H * DPR);
    canvas.style.width = W + 'px';
    canvas.style.height = H + 'px';
    ctx.setTransform(DPR, 0, 0, DPR, 0, 0);
    build();
  }

  function build() {
    var count = Math.min(120, Math.max(48, Math.round((W * H) / 16000)));
    nodes = [];
    for (var i = 0; i < count; i++) {
      nodes.push({
        x: Math.random() * W,
        y: Math.random() * H,
        vx: (Math.random() - 0.5) * 0.24,
        vy: (Math.random() - 0.5) * 0.24,
        r: Math.random() * 1.4 + 0.8,
        c: Math.random() < 0.22 ? '34,211,238' : '129,140,248'
      });
    }
  }

  function spawnPulse() {
    if (reduce || nodes.length < 2 || pulses.length > 9) return;
    var a = nodes[(Math.random() * nodes.length) | 0];
    var best = null, bestD = Infinity;
    for (var k = 0; k < 14; k++) {
      var b = nodes[(Math.random() * nodes.length) | 0];
      if (b === a) continue;
      var dx = a.x - b.x, dy = a.y - b.y;
      var d = Math.sqrt(dx * dx + dy * dy);
      if (d < LINK && d < bestD) { bestD = d; best = b; }
    }
    if (best) pulses.push({ a: a, b: best, t: 0, s: 0.006 + Math.random() * 0.008 });
  }

  function drawPulses() {
    for (var i = pulses.length - 1; i >= 0; i--) {
      var p = pulses[i];
      p.t += p.s;
      if (p.t >= 1) { pulses.splice(i, 1); continue; }
      var x = p.a.x + (p.b.x - p.a.x) * p.t;
      var y = p.a.y + (p.b.y - p.a.y) * p.t;
      var g = ctx.createRadialGradient(x, y, 0, x, y, 8);
      g.addColorStop(0, 'rgba(34,211,238,.95)');
      g.addColorStop(0.4, 'rgba(99,102,241,.45)');
      g.addColorStop(1, 'rgba(99,102,241,0)');
      ctx.fillStyle = g;
      ctx.beginPath();
      ctx.arc(x, y, 8, 0, Math.PI * 2);
      ctx.fill();
    }
  }

  function frame() {
    if (stopped) return;
    ctx.clearRect(0, 0, W, H);

    // 运动
    for (var i = 0; i < nodes.length; i++) {
      var n = nodes[i];
      n.x += n.vx;
      n.y += n.vy;
      if (n.x < -30) n.x = W + 30; else if (n.x > W + 30) n.x = -30;
      if (n.y < -30) n.y = H + 30; else if (n.y > H + 30) n.y = -30;
    }

    // 连线
    ctx.lineWidth = 1;
    for (var a = 0; a < nodes.length; a++) {
      var na = nodes[a];
      for (var b = a + 1; b < nodes.length; b++) {
        var nb = nodes[b];
        var dx = na.x - nb.x, dy = na.y - nb.y;
        var d2 = dx * dx + dy * dy;
        if (d2 > LINK * LINK) continue;

        var d = Math.sqrt(d2);
        var t = 1 - d / LINK;

        // 鼠标附近辉光加成
        var mx = (na.x + nb.x) * 0.5, my = (na.y + nb.y) * 0.5;
        var mdx = mx - mouse.x, mdy = my - mouse.y;
        var md = Math.sqrt(mdx * mdx + mdy * mdy);
        var boost = md < 220 ? (1 - md / 220) * 0.85 : 0;

        var alpha = t * t * 0.26 + t * boost * 0.55;
        if (alpha <= 0.004) continue;

        ctx.strokeStyle = 'rgba(' + na.c + ',' + alpha.toFixed(3) + ')';
        ctx.beginPath();
        ctx.moveTo(na.x, na.y);
        ctx.lineTo(nb.x, nb.y);
        ctx.stroke();
      }
    }

    // 节点
    for (var k = 0; k < nodes.length; k++) {
      var nd = nodes[k];
      var ddx = nd.x - mouse.x, ddy = nd.y - mouse.y;
      var dd = Math.sqrt(ddx * ddx + ddy * ddy);
      var glow = dd < 180 ? (1 - dd / 180) : 0;

      var r = nd.r + glow * 1.5;
      ctx.beginPath();
      ctx.arc(nd.x, nd.y, r, 0, Math.PI * 2);
      ctx.fillStyle = 'rgba(' + nd.c + ',' + (0.5 + glow * 0.5).toFixed(3) + ')';
      ctx.fill();

      if (glow > 0.06) {
        ctx.beginPath();
        ctx.arc(nd.x, nd.y, r + 5, 0, Math.PI * 2);
        ctx.fillStyle = 'rgba(' + nd.c + ',' + (glow * 0.1).toFixed(3) + ')';
        ctx.fill();
      }
    }

    drawPulses();
    // 离开登录页后 canvas 会被卸载：立即停帧，避免继续空转
    if (!canvas.isConnected) return;
    rafId = requestAnimationFrame(frame);
  }

  /* ── 事件 ── */
  function onPointerMove(e) {
    mouse.x = e.clientX;
    mouse.y = e.clientY;

    if (!card || !card.isConnected) return;
    var rect = card.getBoundingClientRect();
    card.style.setProperty('--mx', (((e.clientX - rect.left) / rect.width) * 100) + '%');
    card.style.setProperty('--my', (((e.clientY - rect.top) / rect.height) * 100) + '%');

    // 轻微视差
    if (!scene || !scene.isConnected) return;
    var nx = (e.clientX / window.innerWidth - 0.5);
    var ny = (e.clientY / window.innerHeight - 0.5);
    scene.style.transform =
      'translate3d(' + (-nx * 18).toFixed(1) + 'px,' + (-ny * 12).toFixed(1) + 'px,0)';
  }

  function onPointerLeave() {
    mouse.x = -1e4;
    mouse.y = -1e4;
  }

  window.addEventListener('pointermove', onPointerMove, { passive: true });
  window.addEventListener('pointerleave', onPointerLeave);

  // 表单：演示用 loading
  function onSubmit(e) {
    e.preventDefault();
    var btn = form.querySelector('.btn');
    if (btn.classList.contains('is-loading')) return;
    btn.classList.add('is-loading');
    setTimeout(function () { btn.classList.remove('is-loading'); }, 1600);
  }
  if (form) form.addEventListener('submit', onSubmit);

  // 启动
  resize();
  window.addEventListener('resize', resize);

  if (reduce) {
    // 静态渲染一帧即可
    frame();
  } else {
    rafId = requestAnimationFrame(frame);
    pulseTimer = setInterval(spawnPulse, 420);
  }

  // 卸载清理：事件、rAF、定时器全部释放（否则每次进登录页都会叠加一个 interval）
  return function disposeLoginNet() {
    stopped = true;
    cancelAnimationFrame(rafId);
    clearInterval(pulseTimer);
    window.removeEventListener('pointermove', onPointerMove);
    window.removeEventListener('pointerleave', onPointerLeave);
    if (form) form.removeEventListener('submit', onSubmit);
  };
}
