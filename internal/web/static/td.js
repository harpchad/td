// td keyboard model.
//
// The keymap is the TUI's, key for key. Section 11 says vim-flavored and
// identical in the web UI, and the point of that is muscle memory: whichever
// client is in front of you takes the same keys.
//
// This is a file rather than an inline script so the Content-Security-Policy
// needs no unsafe-inline and no per-page hash.

(function () {
  'use strict';

  var app = document.querySelector('[data-td-app]');
  if (!app) return;

  // ---- selection -------------------------------------------------------

  function rows() {
    return Array.prototype.slice.call(document.querySelectorAll('.td-row[data-id]'))
      .filter(function (r) { return r.offsetParent !== null; });
  }

  function selectedIndex() {
    var all = rows();
    for (var i = 0; i < all.length; i++) {
      if (all[i].classList.contains('td-row--selected')) return i;
    }
    return -1;
  }

  function select(index) {
    var all = rows();
    if (!all.length) return;
    if (index < 0) index = 0;
    if (index >= all.length) index = all.length - 1;

    for (var i = 0; i < all.length; i++) {
      all[i].classList.toggle('td-row--selected', i === index);
    }
    var row = all[index];
    // Keep the selection on screen without smooth scrolling: the only
    // animation in this product is the caret.
    if (row.scrollIntoView) row.scrollIntoView({ block: 'nearest' });
    app.dataset.selected = row.dataset.id;
  }

  function move(delta) {
    var at = selectedIndex();
    select(at < 0 ? 0 : at + delta);
  }

  function current() {
    var all = rows();
    var at = selectedIndex();
    return at < 0 ? null : all[at];
  }

  // Restore the selection after htmx swaps the list, so completing a task
  // does not throw you back to the top.
  function restore() {
    var wanted = app.dataset.selected;
    var all = rows();
    if (!all.length) return;
    for (var i = 0; i < all.length; i++) {
      if (all[i].dataset.id === wanted) { select(i); return; }
    }
    select(0);
  }

  // ---- actions ---------------------------------------------------------

  // Every action is a button that already exists in the row or the toolbar.
  // The key presses it rather than reimplementing it, so the pointer path
  // and the keyboard path cannot drift.
  function press(selector, scope) {
    var el = (scope || document).querySelector(selector);
    if (el) { el.click(); return true; }
    return false;
  }

  function pressInRow(selector) {
    var row = current();
    return row ? press(selector, row) : false;
  }

  // ---- key handling ----------------------------------------------------

  function typing(target) {
    if (!target) return false;
    var tag = target.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable;
  }

  document.addEventListener('keydown', function (e) {
    if (e.metaKey || e.ctrlKey || e.altKey) return;

    // While a field has focus it owns every key but Escape.
    if (typing(e.target)) {
      if (e.key === 'Escape') {
        e.target.blur();
        var form = e.target.closest('form');
        if (form && form.dataset.tdCancel !== undefined) form.reset();
        if (form && form.dataset.saveForm !== undefined) form.hidden = true;
      }
      return;
    }

    var handled = true;
    switch (e.key) {
      case 'j': case 'ArrowDown': move(1); break;
      case 'k': case 'ArrowUp': move(-1); break;
      case 'g': select(0); break;
      case 'G': select(rows().length - 1); break;

      case 'Enter': handled = pressInRow('[data-open]'); break;
      case ' ': case 'd': handled = pressInRow('[data-complete]'); break;
      case 'x': handled = pressInRow('[data-drop]'); break;
      case 'z': handled = pressInRow('.td-fold[aria-expanded]'); break;
      case 'Z': handled = foldAll(); break;

      case 'a': handled = focusField('[data-add-input]'); break;
      case 'S': handled = press('[data-save-toggle]'); break;
      case '/': handled = focusField('[data-filter-input]'); break;
      case 'u': handled = press('[data-undo]'); break;
      case 'r': handled = press('[data-reload]'); break;
      case '?': handled = press('[data-help]'); break;
      case 'Escape': handled = press('[data-back]'); break;

      default:
        if (/^[1-9]$/.test(e.key)) {
          handled = press('[data-slot="' + e.key + '"]');
        } else {
          handled = false;
        }
    }

    if (handled) e.preventDefault();
  });

  function focusField(selector) {
    var el = document.querySelector(selector);
    if (!el) return false;
    el.focus();
    if (el.select) el.select();
    return true;
  }

  // Fold every parent in view, or unfold them all if any is open.
  function foldAll() {
    var folds = document.querySelectorAll('.td-fold[aria-expanded]');
    if (!folds.length) return false;
    var anyOpen = false;
    for (var i = 0; i < folds.length; i++) {
      if (folds[i].getAttribute('aria-expanded') === 'true') anyOpen = true;
    }
    for (var j = 0; j < folds.length; j++) {
      if ((folds[j].getAttribute('aria-expanded') === 'true') === anyOpen) folds[j].click();
    }
    return true;
  }

  // The save-filter form stays hidden until asked for. The key presses this
  // same button, so the keyboard path and the pointer path cannot drift.
  document.addEventListener('click', function (e) {
    var toggle = e.target.closest ? e.target.closest('[data-save-toggle]') : null;
    if (!toggle) return;
    var form = document.querySelector('[data-save-form]');
    if (!form) return;
    form.hidden = !form.hidden;
    if (!form.hidden) {
      var name = form.querySelector('[data-save-name]');
      if (name) { name.focus(); if (name.select) name.select(); }
    }
  });

  // A control marked data-autosubmit posts its form when it changes. This
  // lives here rather than in an onchange attribute because an inline
  // handler would put unsafe-inline back into the Content-Security-Policy,
  // and section 12 rules that out.
  document.addEventListener('change', function (e) {
    var el = e.target;
    if (!el || el.dataset === undefined || el.dataset.autosubmit === undefined) return;
    var form = el.form || (el.closest ? el.closest('form') : null);
    if (!form) return;
    if (form.requestSubmit) form.requestSubmit(); else form.submit();
  });

  // ---- pointer ---------------------------------------------------------

  // Clicking a row selects it. The controls inside stop the event so the
  // checkbox does not also move the selection under the pointer.
  document.addEventListener('click', function (e) {
    var row = e.target.closest ? e.target.closest('.td-row[data-id]') : null;
    if (!row) return;
    var all = rows();
    for (var i = 0; i < all.length; i++) {
      if (all[i] === row) { select(i); return; }
    }
  });

  // htmx replaces the list in place; the selection has to survive that.
  document.body.addEventListener('htmx:afterSwap', restore);
  document.body.addEventListener('htmx:afterSettle', restore);

  restore();
})();
