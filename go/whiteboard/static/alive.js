// alive.js — the sentinel. A page's stream sends a heartbeat ($_beat) every
// 5 s; the page calls window.alive.beat() on each (a data-effect on $_beat).
// While the page is visible and no beat has come for LOST ms, the line is
// taken as lost and the whole page goes black and white (mike.css's .bw on
// <html>, with data-ui-lost for anything else that wants to know); the next
// beat (the stream reconnects by itself) brings the colour back.
// It arms on the first beat, so a page with no stream never greys.
(() => {
  if (window.alive) return
  const LOST = 12000
  let last = 0
  const root = document.documentElement
  const show = lost => {
    root.classList.toggle('bw', lost)
    root.toggleAttribute('data-ui-lost', lost)
  }
  window.alive = {
    beat() { last = Date.now(); show(false) },
  }
  setInterval(() => {
    if (last && !document.hidden) show(Date.now() - last > LOST)
  }, 1000)
  // a hidden tab's stream is closed on purpose: coming back, give it a moment
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && last) last = Date.now()
  })
})()
