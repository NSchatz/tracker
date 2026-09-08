package uiverify

// auditJS is the measuring library, injected into the page under test.
//
// Everything it reports is read from the engine's RESOLVED values: getComputedStyle after the
// cascade, custom-property substitution and the media query; getBoundingClientRect after layout;
// document.activeElement after a real keystroke. It declares nothing, styles nothing and consults no
// stylesheet text, which is what keeps it a measurement of what was drawn rather than a restatement
// of what was written.
//
// The contrast maths is WCAG 2.2's, spelled out rather than pulled from a library, so the numbers in
// a failure message can be checked by hand against the specification text committed with the spec.
const auditJS = `
(function () {
  function parseColor(s) {
    if (!s) { return null; }
    var m = s.match(/rgba?\(([^)]+)\)/);
    if (!m) { return null; }
    var parts = m[1].split(/[,\s\/]+/).filter(function (p) { return p.length; }).map(Number);
    if (parts.length < 3) { return null; }
    var a = parts.length > 3 ? parts[3] : 1;
    return { r: parts[0], g: parts[1], b: parts[2], a: isNaN(a) ? 1 : a };
  }

  function over(fg, bg) {
    // Source-over compositing, which is what the engine did to arrive at the pixel.
    var a = fg.a + bg.a * (1 - fg.a);
    if (a === 0) { return { r: 0, g: 0, b: 0, a: 0 }; }
    return {
      r: (fg.r * fg.a + bg.r * bg.a * (1 - fg.a)) / a,
      g: (fg.g * fg.a + bg.g * bg.a * (1 - fg.a)) / a,
      b: (fg.b * fg.a + bg.b * bg.a * (1 - fg.a)) / a,
      a: a
    };
  }

  function channel(c) {
    var s = c / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  }

  function luminance(c) {
    return 0.2126 * channel(c.r) + 0.7152 * channel(c.g) + 0.0722 * channel(c.b);
  }

  function ratio(a, b) {
    var la = luminance(a), lb = luminance(b);
    var hi = Math.max(la, lb), lo = Math.min(la, lb);
    return (hi + 0.05) / (lo + 0.05);
  }

  // effectiveBackground walks up the box the element is painted on, blending each ancestor's
  // resolved background colour, which is what the engine composited to produce the surface behind
  // the glyphs.
  function effectiveBackground(el) {
    var acc = { r: 0, g: 0, b: 0, a: 0 };
    var node = el;
    while (node && node.nodeType === 1) {
      var bg = parseColor(getComputedStyle(node).backgroundColor);
      if (bg && bg.a > 0) {
        acc = acc.a === 0 ? bg : over(acc, bg);
        if (acc.a >= 0.999) { break; }
      }
      node = node.parentElement;
    }
    if (acc.a < 0.999) {
      var canvas = parseColor(getComputedStyle(document.documentElement).backgroundColor);
      var white = { r: 255, g: 255, b: 255, a: 1 };
      var base = (canvas && canvas.a > 0) ? canvas : white;
      acc = acc.a === 0 ? base : over(acc, base);
    }
    return acc;
  }

  function path(el) {
    if (!el || el.nodeType !== 1) { return '(none)'; }
    if (el.id) { return '#' + el.id; }
    var parts = [];
    var node = el;
    for (var depth = 0; node && node.nodeType === 1 && depth < 5; depth++) {
      var seg = node.tagName.toLowerCase();
      if (node.id) { parts.unshift('#' + node.id); break; }
      if (node.className && typeof node.className === 'string') {
        seg += '.' + node.className.trim().split(/\s+/).join('.');
      }
      parts.unshift(seg);
      node = node.parentElement;
    }
    return parts.join(' > ');
  }

  function visible(el) {
    var cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden' || Number(cs.opacity) === 0) { return false; }
    var r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  }

  // ownText is the text this element renders itself, not the text of its descendants: the
  // element whose colour was used is the one whose text node it is.
  function ownText(el) {
    var out = '';
    for (var i = 0; i < el.childNodes.length; i++) {
      var n = el.childNodes[i];
      if (n.nodeType === 3) { out += n.nodeValue; }
    }
    return out.trim();
  }

  var INTERACTIVE = 'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"]), ' +
    '[role="button"], [role="link"], [contenteditable="true"]';

  window.__uiaudit = {
    // Every visible text run on the page, with the ratio the engine's own colours produce.
    textRuns: function () {
      var out = [];
      var all = document.querySelectorAll('body *');
      for (var i = 0; i < all.length; i++) {
        var el = all[i];
        var text = ownText(el);
        if (!text || !visible(el)) { continue; }
        var cs = getComputedStyle(el);
        var fg = parseColor(cs.color);
        if (!fg) { continue; }
        var bg = effectiveBackground(el);
        var composited = fg.a < 1 ? over(fg, bg) : fg;
        var size = parseFloat(cs.fontSize);
        var weight = parseInt(cs.fontWeight, 10) || 400;
        var large = size >= 24 || (size >= 18.66 && weight >= 700);
        out.push({
          path: path(el),
          text: text.length > 60 ? text.slice(0, 60) + '...' : text,
          color: cs.color,
          background: 'rgb(' + Math.round(bg.r) + ', ' + Math.round(bg.g) + ', ' + Math.round(bg.b) + ')',
          fontSize: size,
          fontWeight: weight,
          ratio: Math.round(ratio(composited, bg) * 100) / 100,
          required: large ? 3 : 4.5
        });
      }
      return out;
    },

    // Every interactive control, with the box the engine laid out for it.
    controls: function () {
      var out = [];
      var all = document.querySelectorAll(INTERACTIVE);
      for (var i = 0; i < all.length; i++) {
        var el = all[i];
        if (!visible(el)) { continue; }
        var r = el.getBoundingClientRect();
        out.push({
          path: path(el),
          tag: el.tagName.toLowerCase(),
          width: Math.round(r.width * 100) / 100,
          height: Math.round(r.height * 100) / 100,
          left: Math.round(r.left * 100) / 100,
          right: Math.round(r.right * 100) / 100,
          top: Math.round(r.top * 100) / 100,
          tabIndex: el.tabIndex
        });
      }
      return out;
    },

    // The non-text contrast of a control's own boundary: its border (or its outline while focused)
    // against the surface it is drawn on.
    boundaries: function () {
      var out = [];
      var all = document.querySelectorAll(INTERACTIVE);
      for (var i = 0; i < all.length; i++) {
        var el = all[i];
        if (!visible(el)) { continue; }
        var cs = getComputedStyle(el);
        var width = parseFloat(cs.borderTopWidth) || 0;
        if (width <= 0 || cs.borderTopStyle === 'none') { continue; }
        var col = parseColor(cs.borderTopColor);
        if (!col || col.a === 0) { continue; }
        var behind = effectiveBackground(el.parentElement || document.body);
        var own = parseColor(cs.backgroundColor);
        var surface = (own && own.a > 0) ? over(own, behind) : behind;
        var composited = col.a < 1 ? over(col, surface) : col;
        out.push({
          path: path(el),
          borderColor: cs.borderTopColor,
          surface: 'rgb(' + Math.round(surface.r) + ', ' + Math.round(surface.g) + ', ' + Math.round(surface.b) + ')',
          ratio: Math.round(ratio(composited, surface) * 100) / 100
        });
      }
      return out;
    },

    // The focus indicator actually painted on whatever currently has keyboard focus.
    focusIndicator: function () {
      var el = document.activeElement;
      if (!el || el === document.body) { return { path: '(none)', focused: false }; }
      var cs = getComputedStyle(el);
      var w = parseFloat(cs.outlineWidth) || 0;
      var col = parseColor(cs.outlineColor);
      var behind = effectiveBackground(el.parentElement || document.body);
      var own = parseColor(cs.backgroundColor);
      var surface = (own && own.a > 0) ? over(own, behind) : behind;
      var r = (col && w > 0 && cs.outlineStyle !== 'none') ? ratio(col.a < 1 ? over(col, surface) : col, surface) : 0;
      return {
        path: path(el),
        focused: true,
        outlineStyle: cs.outlineStyle,
        outlineWidth: w,
        outlineColor: cs.outlineColor,
        surface: 'rgb(' + Math.round(surface.r) + ', ' + Math.round(surface.g) + ', ' + Math.round(surface.b) + ')',
        ratio: Math.round(r * 100) / 100
      };
    },

    activePath: function () { return path(document.activeElement); },

    // The page body's own laid-out width against the space it had.
    reflow: function () {
      var doc = document.documentElement;
      return {
        scrollWidth: doc.scrollWidth,
        clientWidth: doc.clientWidth,
        bodyScrollWidth: document.body.scrollWidth,
        innerWidth: window.innerWidth
      };
    },

    // The backgrounds the engine painted, so "did it render light or dark" is answered by the
    // rendering rather than by looking for a class the page might or might not have added.
    themeBackgrounds: function () {
      function report(sel) {
        var el = document.querySelector(sel);
        if (!el) { return null; }
        var bg = effectiveBackground(el);
        return {
          selector: sel,
          background: 'rgb(' + Math.round(bg.r) + ', ' + Math.round(bg.g) + ', ' + Math.round(bg.b) + ')',
          luminance: Math.round(luminance(bg) * 10000) / 10000
        };
      }
      return [report('body'), report('#panel'), report('#bar')].filter(Boolean);
    },

    // One entry per region the page declares as needing explanation, with the links inside it.
    regions: function () {
      var out = [];
      var regions = document.querySelectorAll('[data-region]');
      for (var i = 0; i < regions.length; i++) {
        var el = regions[i];
        var links = el.querySelectorAll('a[href]');
        var hrefs = [];
        for (var j = 0; j < links.length; j++) { hrefs.push(links[j].getAttribute('href')); }
        out.push({ region: el.getAttribute('data-region'), path: path(el), hrefs: hrefs });
      }
      return out;
    },

    // Every chrome text this page authors, for the "labels stay to a few words" measurement.
    // Device NAMES are excluded by class: they are family data the server supplied, not a label
    // this page wrote, and their length is not this page's to control.
    chromeTexts: function () {
      var out = [];
      var scopes = document.querySelectorAll('[data-region]');
      for (var s = 0; s < scopes.length; s++) {
        var all = scopes[s].querySelectorAll('*');
        for (var i = 0; i < all.length; i++) {
          var el = all[i];
          if (el.classList.contains('name')) { continue; }
          var text = ownText(el);
          if (!text || !visible(el)) { continue; }
          out.push({
            path: path(el),
            text: text,
            words: text.split(/\s+/).filter(function (w) { return w.length; }).length
          });
        }
      }
      return out;
    },

    // The rendered text of every device row, which is what "distinguishable without colour" is
    // measured on.
    deviceRows: function () {
      var out = [];
      var rows = document.querySelectorAll('#panel li');
      for (var i = 0; i < rows.length; i++) {
        var li = rows[i];
        if (!visible(li)) { continue; }
        out.push({
          id: li.getAttribute('data-device-id'),
          state: li.getAttribute('data-state'),
          text: (li.innerText || li.textContent || '').replace(/\s+/g, ' ').trim(),
          list: li.parentElement ? li.parentElement.id : ''
        });
      }
      return out;
    },

    // The single state this view is in, read off the rendered element rather than off a variable.
    panelState: function () {
      var el = document.getElementById('panel-state');
      if (!el) { return null; }
      return {
        state: el.getAttribute('data-state'),
        text: (el.innerText || el.textContent || '').trim(),
        action: (function () {
          var a = document.getElementById('panel-action');
          return a && visible(a) ? (a.innerText || a.textContent || '').trim() : '';
        })(),
        visible: visible(el)
      };
    },

    figures: function () {
      function txt(id) {
        var el = document.getElementById(id);
        if (!el || !visible(el)) { return ''; }
        return (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim();
      }
      return {
        located: txt('located-count'),
        rejected: txt('rejected'),
        rejectedDetail: txt('rejected-detail'),
        status: txt('status')
      };
    }
  };
})();
`
