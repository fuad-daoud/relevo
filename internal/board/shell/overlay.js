// overlay.js -- the script that runs inside the board's frame.
//
// The board renders in <iframe sandbox="allow-scripts">, an opaque origin: the
// shell cannot reach in, and neither can this script read anything on the shell,
// so every crossing is a postMessage in both directions. It must never write a
// note: the board's own scripts share this frame, so the shell treats every
// message here as a claim, and only its own draft box posts.
(function () {
  "use strict";

  var parent = window.parent;
  var mode = false;
  var pins = [];             // the threads the shell last sent, each with its entries
  var pinLayer = null;
  var hovered = null;

  // installStyles injects the rules the overlay needs, because the board is a
  // stranger's document: a class with no rule draws nothing there. The outline's
  // negative offset takes it out of layout, so hovering never reflows the board,
  // and Highlight rather than a fixed blue keeps it right on a board that brought
  // its own theme. A pin is a solid dot, not a numbered badge: it names a
  // thread, and a count would renumber itself on every note that arrived.
  function installStyles() {
    var style = document.createElement("style");
    style.textContent =
      ".relevo-hover{outline:2px solid Highlight;outline-offset:-2px;cursor:pointer;}" +
      ".relevo-pins{position:fixed;inset:0;pointer-events:none;z-index:2147483647;}" +
      ".relevo-pin{position:absolute;pointer-events:auto;width:0.9em;height:0.9em;" +
      "margin:-0.45em 0 0 -0.45em;padding:0;border-radius:50%;background:#2a6fb5;" +
      "box-shadow:0 0 0 2px rgba(255,255,255,0.9);cursor:pointer;}" +
      ".relevo-board-pin{background:#5a5a5a;}" +
      ".relevo-orphan-pin{background:#8a5a00;outline:1px dashed #fff;}";
    (document.head || document.documentElement).appendChild(style);
  }

  // post is every message out of this frame. The origin is "*" because the
  // sandbox took the shell's own, and nothing here is secret.
  function post(msg) {
    parent.postMessage(msg, "*");
  }

  window.addEventListener("message", function (ev) {
    if (ev.source !== parent) return;
    var msg = ev.data;
    if (!msg || typeof msg !== "object") return;
    if (msg.type === "relevo.mode") setMode(msg.on === true);
    else if (msg.type === "relevo.pins") setPins(msg.annotations);
    else if (msg.type === "relevo.points") post({ type: "relevo.points", points: points() });
  });

  // selectorFor builds a note's anchor in a fixed order, so one element always
  // yields one string: a data-board-id, then a unique id, then a path.
  function selectorFor(el) {
    if (!el || el.nodeType !== 1) return "";
    var host = el.closest("[data-board-id]");
    if (host) return '[data-board-id="' + escapeAttr(host.getAttribute("data-board-id")) + '"]';
    if (el.id && isUniqueId(el.id)) return "#" + escapeAttr(el.id);
    return pathFor(el);
  }

  function isUniqueId(id) {
    var nodes;
    try {
      nodes = document.querySelectorAll("[id]");
    } catch (e) {
      return false;
    }
    var seen = 0;
    for (var i = 0; i < nodes.length; i++) {
      if (nodes[i].id === id && ++seen > 1) return false;
    }
    return seen === 1;
  }

  // pathFor steps down from the nearest ancestor carrying an id or a
  // data-board-id, as tag:nth-of-type(n) read live so a sibling the board's own
  // script adds later does not break the path.
  function pathFor(el) {
    var steps = [];
    var node = el;
    while (node && node.nodeType === 1 && node !== document.body) {
      steps.unshift(tagStep(node));
      var up = node.parentElement;
      if (!up) break;
      if (up.id || up.hasAttribute("data-board-id")) break;
      node = up;
    }
    var anchor = node && node.parentElement;
    var root = "";
    if (anchor && anchor.hasAttribute("data-board-id")) {
      root = '[data-board-id="' + escapeAttr(anchor.getAttribute("data-board-id")) + '"] > ';
    } else if (anchor && anchor.id) {
      root = "#" + escapeAttr(anchor.id) + " > ";
    }
    return root + steps.join(" > ");
  }

  function tagStep(el) {
    var tag = el.tagName.toLowerCase();
    if (!el.parentElement) return tag;
    var index = 1;
    var siblings = el.parentElement.children;
    for (var i = 0; i < siblings.length; i++) {
      if (siblings[i].tagName === el.tagName) {
        if (siblings[i] === el) break;
        index++;
      }
    }
    return tag + ":nth-of-type(" + index + ")";
  }

  function escapeAttr(value) {
    return String(value == null ? "" : value).replace(/["\\]/g, "\\$&");
  }

  // pick reports fractions of the element's box rather than pixels, which makes
  // the pin survive a resize.
  function pick(el, clientX, clientY) {
    var box = el.getBoundingClientRect();
    var x = box.width ? (clientX - box.left) / box.width : 0;
    var y = box.height ? (clientY - box.top) / box.height : 0;
    post({ type: "relevo.pick", selector: selectorFor(el), x: clamp(x), y: clamp(y) });
  }

  // clamp keeps a fraction inside the unit square: a border click reports one
  // slightly outside it.
  function clamp(v) {
    if (typeof v !== "number" || !isFinite(v)) return 0;
    return v < 0 ? 0 : v > 1 ? 1 : v;
  }

  // setPins holds what the shell sent, grouped by anchor: the same selector
  // string is one thread, an empty selector is the board. Presentation only, so
  // the stored file stays flat.
  function setPins(entries) {
    pins = [];
    var by = {};
    for (var i = 0; i < (entries || []).length; i++) {
      var entry = entries[i];
      var key = "$" + (entry.selector || "");
      var thread = by[key];
      if (!thread) {
        thread = by[key] = { key: entry.selector || "", id: entry.id, entries: [] };
        pins.push(thread);
      }
      thread.entries.push(entry);
    }
    draw();
  }

  // draw puts one pin per thread on the board, and only in comment mode: out of
  // it the board is the board again, so the layer goes back off the document. An
  // anchor matching nothing, or throwing, is an orphan -- still drawn, still
  // listed, still reported by id, because a note whose element moved is a note.
  function draw() {
    if (!mode) {
      if (pinLayer && pinLayer.parentNode) pinLayer.parentNode.removeChild(pinLayer);
      pinLayer = null;
      return;
    }
    if (!pinLayer) {
      pinLayer = document.createElement("div");
      pinLayer.className = "relevo-pins";
      (document.body || document.documentElement).appendChild(pinLayer);
    }
    pinLayer.textContent = "";
    var orphans = [];
    for (var i = 0; i < pins.length; i++) {
      var el = pins[i].key ? resolve(pins[i].key) : null;
      if (pins[i].key && !el) orphans.push(pins[i].id);
      drawPin(pins[i], el, i);
    }
    post({ type: "relevo.orphans", ids: orphans });
  }

  // resolve finds the element a selector names; one that throws is a miss, not
  // an error, because stored selectors are not trusted to parse.
  function resolve(selector) {
    try {
      return document.querySelector(selector);
    } catch (e) {
      return null;
    }
  }

  // drawPin draws a thread as one dot, named for a reader who cannot see it, and
  // reports its own position on click, which is how the shell finds the pin.
  function drawPin(thread, el, index) {
    var pin = document.createElement("button");
    pin.type = "button";
    pin.className = "relevo-pin";
    pin.setAttribute("data-relevo-id", thread.id);
    var name = thread.entries[0].text || "";
    if (thread.entries.length > 1) name = thread.entries.length + " comments: " + name;
    pin.setAttribute("aria-label", name);
    pin.title = name;
    if (!thread.key) pin.className += " relevo-board-pin";
    else if (!el) pin.className += " relevo-orphan-pin";
    pin.addEventListener("click", function (e) {
      e.preventDefault();
      e.stopPropagation();
      var box = pin.getBoundingClientRect();
      post({
        type: "relevo.pin", id: thread.id,
        x: Math.round(box.left + box.width / 2),
        y: Math.round(box.top + box.height / 2)
      });
    });
    pinLayer.appendChild(pin);
    placePin(pin, thread, el, index);
  }

  function placePin(pin, thread, el, index) {
    if (!el) {
      pin.style.left = "8px";
      pin.style.top = (8 + index * 26) + "px";
      return;
    }
    var box = el.getBoundingClientRect();
    var first = thread.entries[0];
    pin.style.left = (box.left + box.width * clamp(first.x)) + "px";
    pin.style.top = (box.top + box.height * clamp(first.y)) + "px";
  }

  function reposition() {
    if (!pinLayer) return;
    for (var i = 0; i < pins.length; i++) {
      var pin = pinLayer.querySelector('[data-relevo-id="' + pins[i].id + '"]');
      if (!pin) continue;
      placePin(pin, pins[i], pins[i].key ? resolve(pins[i].key) : null, i);
    }
  }

  // points reports where each pin sits, so the shell can anchor a card beside
  // one from the placed pin's own box rather than recomputing it.
  function points() {
    var out = [];
    if (!pinLayer) return out;
    for (var i = 0; i < pins.length; i++) {
      var pin = pinLayer.querySelector('[data-relevo-id="' + pins[i].id + '"]');
      if (!pin) continue;
      var box = pin.getBoundingClientRect();
      out.push({
        id: pins[i].id,
        x: Math.round(box.left + box.width / 2),
        y: Math.round(box.top + box.height / 2)
      });
    }
    return out;
  }

  window.addEventListener("scroll", reposition, true);
  window.addEventListener("resize", reposition);

  // outline keeps exactly one element marked, the last one hovered; mode-off
  // passes null and leaves the board as found.
  function outline(el) {
    if (hovered === el) return;
    if (hovered && hovered.classList) hovered.classList.remove("relevo-hover");
    hovered = el && el.nodeType === 1 ? el : null;
    if (hovered) hovered.classList.add("relevo-hover");
  }

  document.addEventListener("mouseover", function (e) {
    if (mode) outline(e.target);
  }, true);

  document.addEventListener("mouseleave", function () { outline(null); }, true);

  // The click is captured, default action and propagation both stopped, so a
  // board's own link does not navigate. A click on our own pin is let through:
  // stopping propagation this early would leave the pin unclickable.
  function onClick(e) {
    var el = e.target;
    if (el && el.closest && el.closest(".relevo-pin")) return;
    e.preventDefault();
    e.stopPropagation();
    pick(e.target, e.clientX, e.clientY);
  }

  // setMode turns comment mode on and off. Off takes the click listener and the
  // pins back off the board; on puts back the threads already held here.
  function setMode(on) {
    mode = on === true;
    if (mode) {
      document.addEventListener("click", onClick, true);
    } else {
      document.removeEventListener("click", onClick, true);
      outline(null);
    }
    draw();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", installStyles);
  } else {
    installStyles();
  }
  post({ type: "relevo.overlayReady" });
})();
