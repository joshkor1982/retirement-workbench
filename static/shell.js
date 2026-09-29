/* Army Retirement Workbench shell: theme switch, mobile menu, remembered how-tos.
   The sidebar itself is server-rendered; this only adds behavior. */
(function () {
  'use strict';
  var KEY = 'rwTheme';

  function resolve(mode) {
    if (mode === 'light' || mode === 'dark') return mode;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }
  function stored() {
    try { return localStorage.getItem(KEY) || 'system'; } catch (e) { return 'system'; }
  }
  function wireTheme() {
    var btns = document.querySelectorAll('.theme-seg button');
    function reflect(mode) {
      btns.forEach(function (b) { b.setAttribute('aria-pressed', b.dataset.mode === mode ? 'true' : 'false'); });
    }
    btns.forEach(function (b) {
      b.addEventListener('click', function () {
        var mode = b.dataset.mode;
        try { localStorage.setItem(KEY, mode); } catch (e) {}
        document.documentElement.setAttribute('data-theme', resolve(mode));
        reflect(mode);
      });
    });
    reflect(stored());
    if (window.matchMedia) {
      var mq = window.matchMedia('(prefers-color-scheme: dark)');
      var follow = function () { if (stored() === 'system') document.documentElement.setAttribute('data-theme', resolve('system')); };
      if (mq.addEventListener) mq.addEventListener('change', follow);
    }
  }

  function wireMenu() {
    var btn = document.getElementById('menu-btn'), nav = document.getElementById('sidenav'), scrim = document.getElementById('scrim');
    if (!btn || !nav) return;
    function set(open) {
      document.body.classList.toggle('nav-open', open);
      btn.setAttribute('aria-expanded', open ? 'true' : 'false');
      scrim.hidden = !open;
    }
    btn.addEventListener('click', function () { set(!document.body.classList.contains('nav-open')); });
    scrim.addEventListener('click', function () { set(false); });
    document.addEventListener('keydown', function (e) { if (e.key === 'Escape') set(false); });
  }

  // "How to Use" disclosures remember open or closed per page.
  function wireHowTos() {
    document.querySelectorAll('details.how-to').forEach(function (d, i) {
      var sum = d.querySelector('summary');
      var key = 'rwHowTo:' + location.pathname + ':' + (sum ? sum.textContent.trim() : i);
      try {
        var saved = localStorage.getItem(key);
        if (saved === 'open') d.open = true;
        else if (saved === 'closed') d.open = false;
      } catch (e) {}
      d.addEventListener('toggle', function () {
        try { localStorage.setItem(key, d.open ? 'open' : 'closed'); } catch (e) {}
      });
    });
  }

  // One-shot notices the server leaves in the rw_flash cookie after a save.
  function toast(kind, msg) {
    var box = document.getElementById('toasts');
    if (!box) {
      box = document.createElement('div');
      box.id = 'toasts'; box.className = 'toasts';
      box.setAttribute('role', 'status'); box.setAttribute('aria-live', 'polite');
      document.body.appendChild(box);
    }
    var t = document.createElement('div');
    t.className = 'toast toast-' + (kind === 'err' ? 'err' : 'ok');
    t.textContent = msg;
    box.appendChild(t);
    requestAnimationFrame(function () { t.classList.add('in'); });
    setTimeout(function () { t.classList.remove('in'); setTimeout(function () { t.remove(); }, 250); }, kind === 'err' ? 7000 : 3500);
  }
  window.rwToast = toast;
  function readFlash() {
    var m = document.cookie.match(/(?:^|; )rw_flash=([^;]*)/);
    if (!m) return '';
    document.cookie = 'rw_flash=; Max-Age=0; Path=/; SameSite=Strict';
    var raw = decodeURIComponent(m[1].replace(/\+/g, ' '));
    var cut = raw.indexOf('|');
    toast(raw.slice(0, cut), raw.slice(cut + 1));
    return raw.slice(0, cut);
  }

  // Quick search: Cmd+K / Ctrl+K or "/" opens a palette over any page.
  function wireSearch() {
    var btn = document.getElementById('search-btn');
    var kbd = document.getElementById('search-kbd');
    if (kbd && !/Mac|iPhone|iPad/.test(navigator.platform)) kbd.textContent = 'Ctrl K';
    var wrap, input, list, items = [], sel = 0, timer, seq = 0;
    function build() {
      wrap = document.createElement('div');
      wrap.className = 'palette'; wrap.hidden = true;
      wrap.innerHTML = '<div class="palette-box" role="dialog" aria-modal="true" aria-label="Search everything">' +
        '<input class="palette-input" type="search" placeholder="Search tasks, notes, documents, pages..." autocomplete="off" aria-label="Search">' +
        '<ul class="palette-list" role="listbox"></ul>' +
        '<div class="palette-foot"><span><kbd>↑</kbd><kbd>↓</kbd> move</span><span><kbd>Enter</kbd> open</span><span><kbd>Esc</kbd> close</span></div></div>';
      document.body.appendChild(wrap);
      input = wrap.querySelector('input'); list = wrap.querySelector('ul');
      wrap.addEventListener('mousedown', function (e) { if (e.target === wrap) close(); });
      input.addEventListener('input', function () { clearTimeout(timer); timer = setTimeout(run, 90); });
      input.addEventListener('keydown', function (e) {
        if (e.key === 'ArrowDown') { e.preventDefault(); move(1); }
        else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1); }
        else if (e.key === 'Enter') { e.preventDefault(); go(items[sel]); }
        else if (e.key === 'Escape') { close(); }
      });
    }
    function render() {
      list.textContent = '';
      if (!items.length) {
        var li = document.createElement('li'); li.className = 'palette-empty';
        li.textContent = input.value.trim() ? 'Nothing matches.' : 'Type to search everything in the app.';
        list.appendChild(li); return;
      }
      items.forEach(function (h, i) {
        var li = document.createElement('li');
        li.className = 'palette-item' + (i === sel ? ' sel' : '');
        li.setAttribute('role', 'option');
        var k = document.createElement('span'); k.className = 'palette-kind'; k.textContent = h.kind;
        var t = document.createElement('span'); t.className = 'palette-title'; t.textContent = h.title + (h.ext ? ' ↗' : '');
        li.appendChild(k); li.appendChild(t);
        if (h.sub) { var sub = document.createElement('span'); sub.className = 'palette-sub'; sub.textContent = h.sub; li.appendChild(sub); }
        li.addEventListener('mousemove', function () { if (sel !== i) { sel = i; mark(); } });
        li.addEventListener('click', function () { go(h); });
        list.appendChild(li);
      });
    }
    function mark() { [].forEach.call(list.children, function (li, i) { li.classList.toggle('sel', i === sel); }); }
    function move(d) { if (!items.length) return; sel = (sel + d + items.length) % items.length; mark(); list.children[sel].scrollIntoView({ block: 'nearest' }); }
    function run() {
      var q = input.value.trim(), mine = ++seq;
      if (!q) { items = []; render(); return; }
      fetch('/search?q=' + encodeURIComponent(q)).then(function (r) { return r.json(); }).then(function (d) {
        if (mine !== seq) return; items = d; sel = 0; render();
      }).catch(function () {});
    }
    function go(h) {
      if (!h) return;
      close();
      if (h.ext) window.open(h.href, '_blank', 'noopener'); else location.href = h.href;
    }
    function open() {
      if (!wrap) build();
      wrap.hidden = false; document.body.classList.add('palette-open');
      input.value = ''; items = []; render(); input.focus();
    }
    function close() { if (wrap) { wrap.hidden = true; document.body.classList.remove('palette-open'); } }
    if (btn) btn.addEventListener('click', open);
    document.addEventListener('keydown', function (e) {
      var typing = /INPUT|TEXTAREA|SELECT/.test((e.target || {}).tagName || '') || (e.target && e.target.isContentEditable);
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); wrap && !wrap.hidden ? close() : open(); }
      else if (e.key === '/' && !typing) { e.preventDefault(); open(); }
    });
  }

  var calm = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // Numbers marked data-countup climb from 0 to their value on load.
  function countUp(el, to, ms) {
    var suffix = el.dataset.suffix || '', t0 = null;
    if (calm || !(to > 0)) { el.textContent = to + suffix; return; }
    function step(t) {
      if (t0 === null) t0 = t;
      var k = Math.min(1, (t - t0) / ms), eased = 1 - Math.pow(1 - k, 3);
      el.textContent = Math.round(to * eased) + suffix;
      if (k < 1) requestAnimationFrame(step);
    }
    el.textContent = '0' + suffix;
    requestAnimationFrame(step);
    // Animation frames pause in hidden tabs; never leave a wrong number showing.
    setTimeout(function () { el.textContent = to + suffix; }, ms + 400);
  }
  function wireCountUps() {
    // ?analyzed=1 marks the one load that should play; drop it so a refresh does not.
    if (/[?&]analyzed=1/.test(location.search)) history.replaceState(null, '', location.pathname + location.hash);
    document.querySelectorAll('[data-countup]').forEach(function (el) {
      var to = parseInt(el.dataset.countup, 10);
      if (!isNaN(to)) countUp(el, to, to > 200 ? 900 : 1100);
    });
  }

  // While the claim analysis runs (it can take a minute), the rating square
  // climbs slowly from 0 so it is clearly working, never reaching 100.
  function wireAnalyze() {
    document.querySelectorAll('form.analyze-form').forEach(function (f) {
      f.addEventListener('submit', function () {
        var btn = f.querySelector('button');
        if (btn) { btn.disabled = true; btn.textContent = 'Analyzing...'; }
        var sq = document.querySelector('.rating-square'), num = sq && sq.querySelector('.rating-num'), cap = sq && sq.querySelector('.rating-cap');
        if (!num) return;
        sq.classList.add('rating-working');
        if (cap) cap.textContent = 'analyzing';
        var v = 0;
        num.textContent = '0%';
        if (calm) return;
        (function tick() {
          v += (95 - v) * 0.035; // eases toward 95 and never gets there
          num.textContent = Math.floor(v) + '%';
          setTimeout(tick, 120);
        })();
      });
    });
  }

  // Card headers get an icon matched to their topic; anything unmatched
  // takes its page's sidebar icon. Icons come from nav.go via #rw-icons.
  var HEAD_ICONS = [
    [/numbers/i, 'chart'], [/week|appointment|calendar/i, 'calendar'], [/countdown|leave|clock/i, 'clock'],
    [/next up|to-do|one list|checklist|packet file/i, 'check'], [/note/i, 'note'],
    [/document|form|shelf|record/i, 'file'], [/medical|rating|condition|medication|symptom|claim/i, 'heart'],
    [/par\b|submit|packet/i, 'send'], [/money|saving|budget|bill|debt|payoff|pay|month/i, 'dollar'],
    [/resume|target|header/i, 'resume'], [/skillbridge|program|lead|application/i, 'bridge'],
    [/job|prospect|opening|search/i, 'briefcase'], [/contact|who to call|about you/i, 'user'],
    [/home|housing/i, 'house'], [/resource|link|site/i, 'link'], [/itp|transition plan|readiness|standards/i, 'compass'],
    [/timeline|glide/i, 'timeline'], [/settings|data|advisor/i, 'settings']
  ];
  function wireHeadIcons() {
    var src = document.getElementById('rw-icons');
    if (!src) return;
    var icons; try { icons = JSON.parse(src.textContent); } catch (e) { return; }
    var fallback = document.body.dataset.pageIcon;
    document.querySelectorAll('main .card > h2, main .card > .job-results-head > h2').forEach(function (h) {
      if (h.querySelector('.h-icon')) return;
      var text = h.textContent, name = fallback;
      for (var i = 0; i < HEAD_ICONS.length; i++) { if (HEAD_ICONS[i][0].test(text)) { name = HEAD_ICONS[i][1]; break; } }
      if (!icons[name]) return;
      var svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('aria-hidden', 'true'); svg.setAttribute('class', 'h-icon');
      svg.innerHTML = icons[name]; // trusted, compiled into the app
      h.insertBefore(svg, h.firstChild);
      h.classList.add('has-icon');
    });
  }

  // Save buttons turn green for a moment once the save lands. The page
  // reloads after a save, so the button is remembered across the reload and
  // lit only when the server confirmed success (an "ok" notice).
  var SAVED_KEY = 'rwSaved';
  function wireSaved(flashKind) {
    document.addEventListener('submit', function (e) {
      var b = e.submitter;
      if (b && /^Save\b/.test(b.textContent.trim())) {
        try { sessionStorage.setItem(SAVED_KEY, (e.target.getAttribute('action') || '') + '|' + b.textContent.trim()); } catch (x) {}
      }
    }, true);
    var saved; try { saved = sessionStorage.getItem(SAVED_KEY); sessionStorage.removeItem(SAVED_KEY); } catch (x) {}
    if (!saved || flashKind === 'err') return;
    var cut = saved.indexOf('|'), action = saved.slice(0, cut), label = saved.slice(cut + 1);
    var btn = [].slice.call(document.querySelectorAll('form')).filter(function (f) { return (f.getAttribute('action') || '') === action; })
      .map(function (f) { return [].slice.call(f.querySelectorAll('button')).filter(function (b) { return b.textContent.trim() === label; })[0]; })
      .filter(Boolean)[0];
    if (!btn) return;
    btn.classList.add('is-saved'); btn.textContent = '✓ Saved';
    setTimeout(function () { btn.classList.remove('is-saved'); btn.textContent = label; }, 2200);
  }

  function init() { var k = readFlash(); wireTheme(); wireMenu(); wireHowTos(); wireSearch(); wireCountUps(); wireAnalyze(); wireHeadIcons(); wireSaved(k); }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
