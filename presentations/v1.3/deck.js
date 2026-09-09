/* ==========================================================================
   Minimal, dependency-free presentation engine.
   Keyboard-driven, scales a fixed 1280x720 canvas to any viewport,
   supports stepped reveals, speaker notes, and print-to-PDF.
   ========================================================================== */

(function () {
  'use strict';

  var SLIDE_W = 1280;
  var SLIDE_H = 720;

  var stage   = document.getElementById('stage');
  var slides  = Array.prototype.slice.call(document.querySelectorAll('.slide'));
  var progress = document.getElementById('progress');
  var notesBody = document.getElementById('notes-body');

  var current = 0;   // slide index
  var step    = 0;   // fragment step within the current slide

  /* ---- fragment groups ---------------------------------------------------
     Fragments reveal in order. Several elements can share a step by carrying
     the same data-frag value; elements without one get sequential steps. */

  function fragGroups(slide) {
    var els = Array.prototype.slice.call(slide.querySelectorAll('.frag, .frag-dim'));
    if (!els.length) return [];

    var explicit = {};
    var groups = [];

    els.forEach(function (el) {
      var key = el.getAttribute('data-frag');
      if (key === null) {
        groups.push([el]);
      } else if (explicit[key]) {
        explicit[key].push(el);
      } else {
        explicit[key] = [el];
        groups.push(explicit[key]);
      }
    });

    // Explicit data-frag values sort numerically ahead of their DOM position
    // only when *all* fragments on the slide are explicit; otherwise keep DOM order.
    var allExplicit = els.every(function (el) { return el.getAttribute('data-frag') !== null; });
    if (allExplicit) {
      groups.sort(function (a, b) {
        return (+a[0].getAttribute('data-frag')) - (+b[0].getAttribute('data-frag'));
      });
    }
    return groups;
  }

  function applySteps() {
    // Keep the architecture reveal on slide 5. All other slides show their
    // content at once so advancing does not require one click per bullet.
    if (current !== 4) {
      slides[current].querySelectorAll('.frag, .frag-dim').forEach(function (el) {
        el.classList.add('shown');
      });
      return;
    }
    var groups = fragGroups(slides[current]);
    groups.forEach(function (group, i) {
      group.forEach(function (el) { el.classList.toggle('shown', i < step); });
    });
  }

  function maxStep(index) {
    return index === 4 ? fragGroups(slides[index]).length : 0;
  }

  /* ---- navigation -------------------------------------------------------- */

  function show(index, atEnd) {
    index = Math.max(0, Math.min(slides.length - 1, index));
    slides[current].classList.remove('active');
    current = index;
    slides[current].classList.add('active');
    step = atEnd ? maxStep(current) : 0;
    applySteps();
    render();
    location.hash = String(current + 1);
  }

  function next() {
    if (step < maxStep(current)) { step++; applySteps(); render(); }
    else if (current < slides.length - 1) { show(current + 1); }
  }

  function prev() {
    if (step > 0) { step--; applySteps(); render(); }
    else if (current > 0) { show(current - 1, true); }
  }

  function render() {
    progress.style.width = ((current + 1) / slides.length * 100) + '%';

    var num = slides[current].querySelector('.slide-footer .num');
    if (num) num.textContent = (current + 1) + ' / ' + slides.length;

    var src = slides[current].querySelector('.notes-src');
    notesBody.innerHTML = src
      ? src.innerHTML
      : '<p style="color:var(--ink-faint)">No notes for this slide.</p>';
  }

  /* ---- scaling ----------------------------------------------------------- */

  function fit() {
    var availW = window.innerWidth - (document.body.classList.contains('notes-on') ? 380 : 0);
    var availH = window.innerHeight;
    var scale  = Math.min(availW / SLIDE_W, availH / SLIDE_H);
    var offset = document.body.classList.contains('notes-on') ? -190 : 0;
    stage.style.transform =
      'translate(-50%, -50%) translateX(' + offset + 'px) scale(' + scale + ')';
  }

  /* ---- keyboard ---------------------------------------------------------- */

  document.addEventListener('keydown', function (e) {
    if (e.metaKey || e.ctrlKey || e.altKey) return;

    switch (e.key) {
      case 'ArrowRight': case 'ArrowDown': case ' ': case 'PageDown':
        e.preventDefault(); next(); break;

      case 'ArrowLeft': case 'ArrowUp': case 'PageUp':
        e.preventDefault(); prev(); break;

      case 'n': case 'N':
        e.preventDefault(); show(current + 1); break;

      case 'p': case 'P':
        e.preventDefault(); show(current - 1); break;

      case 'Home': e.preventDefault(); show(0); break;
      case 'End':  e.preventDefault(); show(slides.length - 1, true); break;

      case 's': case 'S':
        document.body.classList.toggle('notes-on'); fit(); break;

      case 'f': case 'F':
        if (document.fullscreenElement) document.exitFullscreen();
        else document.documentElement.requestFullscreen();
        break;

      case 'b': case 'B':
        document.body.style.visibility =
          document.body.style.visibility === 'hidden' ? '' : 'hidden';
        break;

      case '?':
        document.body.classList.toggle('help-on'); break;

      case 'Escape':
        document.body.classList.remove('help-on'); break;

      default:
        if (/^[1-9]$/.test(e.key)) show(parseInt(e.key, 10) - 1);
    }
  });

  /* ---- pointer / touch --------------------------------------------------- */

  document.addEventListener('click', function (e) {
    if (document.body.classList.contains('help-on')) {
      document.body.classList.remove('help-on');
      return;
    }
    if (e.target.closest('#notes') || e.target.closest('video')) return;
    (e.clientX < window.innerWidth * 0.25 ? prev : next)();
  });

  var touchX = null;
  document.addEventListener('touchstart', function (e) { touchX = e.touches[0].clientX; }, { passive: true });
  document.addEventListener('touchend', function (e) {
    if (touchX === null) return;
    var dx = e.changedTouches[0].clientX - touchX;
    if (Math.abs(dx) > 45) (dx < 0 ? next : prev)();
    touchX = null;
  }, { passive: true });

  /* ---- print ------------------------------------------------------------- */

  window.addEventListener('beforeprint', function () {
    slides.forEach(function (s) {
      s.classList.add('active');
      s.querySelectorAll('.frag, .frag-dim').forEach(function (f) { f.classList.add('shown'); });
      var num = s.querySelector('.slide-footer .num');
      if (num) num.textContent = (slides.indexOf(s) + 1) + ' / ' + slides.length;
    });
  });

  window.addEventListener('afterprint', function () {
    slides.forEach(function (s, i) {
      s.classList.toggle('active', i === current);
      if (i !== current) {
        s.querySelectorAll('.frag, .frag-dim').forEach(function (f) { f.classList.remove('shown'); });
      }
    });
    applySteps();
  });

  /* ---- boot -------------------------------------------------------------- */

  window.addEventListener('resize', fit);

  var fromHash = parseInt((location.hash || '').replace('#', ''), 10);
  current = (fromHash >= 1 && fromHash <= slides.length) ? fromHash - 1 : 0;

  slides.forEach(function (s, i) { s.classList.toggle('active', i === current); });
  applySteps();
  render();
  fit();
})();
