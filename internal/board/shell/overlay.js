// overlay.js -- the script that runs inside the board's frame.
//
// The board renders in <iframe sandbox="allow-scripts">, an opaque origin. The
// shell cannot reach in, and neither can this script reach out to read anything
// on the shell: every crossing goes through postMessage, in both directions.
// This script does what the shell cannot: outline the hovered element, report a
// click as a pick, draw the pins, and report where the pins sit.
//
// What it must never do is write a note. A board's own scripts share this frame
// and can post a pick, so the shell treats every message from here as a claim,
// not a fact: only the shell's draft box posts, and only the shell holds the
// token. Everything below posts; nothing here fetches.
(function () {
  "use strict";

  var parent = window.parent;
  var mode = false;
  var pins = [];
  var pinLayer = null;
  var hovered = null;

  // installStyles adds the rules this overlay needs. The board is a stranger's
  // document, so a class with no rule draws nothing; these are injected here
  // because the class is applied inside the frame, where the shell's stylesheet
  // does not reach.
  //
  // The outline's negative offset takes it out of layout: hovering never
  // reflows the board and a sibling's box is unchanged while it shows. The
  // accent is the system Highlight colour rather than a fixed blue, because the
  // board brings its own theme and a hardcoded colour reads as a bug on
  // whichever background it chose.
  function installStyles() {
    var style = document.createElement("style");
    style.textContent =
      ".relevo-hover{outline:2px solid Highlight;outline-offset:-2px;cursor:pointer;}" +
      ".relevo-pins{position:fixed;inset:0;pointer-events:none;z-index:2147483647;}" +
      ".relevo-pin{position:absolute;pointer-events:auto;min-width:1.35em;height:1.35em;" +
      "margin:-0.68em 0 0 -0.68em;padding:0;border-radius:50%;" +
      "background:#2a6fb5;color:#fff;font:600 11px/1.35em ui-sans-serif,system-ui,sans-serif;" +
      "cursor:pointer;}" +
      ".relevo-board-pin{background:#5a5a5a;}" +
      ".relevo-orphan-pin{background:#8a5a00;outline:1px dashed #fff;}";
    (document.head || document.documentElement).appendChild(style);
  }

  // post is every message out of this frame. The target origin is "*" because
  // the shell is unreadable from here -- the sandbox took its origin -- and
  // nothing here is secret: a selector, a fraction, an id, a point.
  function post(msg) {
    parent.postMessage(msg, "*");
  }

  // fromShell is every message in. Only the parent's own messages are acted on.
  window.addEventListener("message", function (ev) {
    if (ev.source !== parent) return;
    var msg = ev.data;
    if (!msg || typeof msg !== "object") return;
    if (msg.type === "relevo.mode") setMode(msg.on === true);
    else if (msg.type === "relevo.pins") setPins(msg.annotations);
    else if (msg.type === "relevo.points") post({ type: "relevo.points", points: points() });
  });

  // selectorFor builds the selector a note is anchored to, in a fixed order so
  // one element always yields one string: a declared data-board-id first, then a
  // unique id, then a structural path. The path always resolves, so even a click
  // on an anonymous element is anchored to something.
  function selectorFor(el) {
    if (!el || el.nodeType !== 1) return "";
    var host = el.closest("[data-board-id]");
    if (host) return '[data-board-id="' + escapeAttr(host.getAttribute("data-board-id")) + '"]';
    if (el.id && isUniqueId(el.id)) return "#" + escapeAttr(el.id);
    return pathFor(el);
  }

  // isUniqueId reports whether an id appears exactly once, so #id names this
  // element rather than a name shared with a dozen others.
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
  // data-board-id -- or from body when there is none -- as tag:nth-of-type(n).
  // The index is read live, so a path still resolves after the board's own
  // script adds a sibling.
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

  // tagStep is one path step: the tag plus its position among its same-tag
  // siblings, which is what makes the step unambiguous.
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

  // escapeAttr quotes the two characters that would end the quoted part of a
  // selector, so a data-board-id carrying a quote still parses.
  function escapeAttr(value) {
    return String(value == null ? "" : value).replace(/["\\]/g, "\\$&");
  }

  // pick reports the element a click landed on, with x and y as fractions of that
  // element's box. A fraction rather than a pixel is what makes the pin survive
  // a resize.
  function pick(el, clientX, clientY) {
    var box = el.getBoundingClientRect();
    var x = box.width ? (clientX - box.left) / box.width : 0;
    var y = box.height ? (clientY - box.top) / box.height : 0;
    post({ type: "relevo.pick", selector: selectorFor(el), x: clamp(x), y: clamp(y) });
  }

  // clamp keeps a fraction inside the unit square: a click on a border can
  // report a box fraction slightly outside it.
  function clamp(v) {
    if (typeof v !== "number" || !isFinite(v)) return 0;
    return v < 0 ? 0 : v > 1 ? 1 : v;
  }

  // setPins redraws the pins from the entries the shell sends. An entry whose
  // selector matches nothing, or throws, is an orphan: still drawn, still
  // listed, reported back by id -- a note whose element moved is still a note.
  function setPins(entries) {
    pins = entries && entries.length ? entries : [];
    if (!pinLayer) {
      pinLayer = document.createElement("div");
      pinLayer.className = "relevo-pins";
      (document.body || document.documentElement).appendChild(pinLayer);
    }
    pinLayer.textContent = "";
    var orphans = [];
    for (var i = 0; i < pins.length; i++) {
      var el = pins[i].selector ? resolve(pins[i].selector) : null;
      if (pins[i].selector && !el) orphans.push(pins[i].id);
      drawPin(pins[i], el, i + 1);
    }
    post({ type: "relevo.orphans", ids: orphans });
  }

  // resolve finds the element a selector names. A selector that throws is a
  // miss, not an error: stored selectors are not trusted to parse.
  function resolve(selector) {
    try {
      return document.querySelector(selector);
    } catch (e) {
      return null;
    }
  }

  // drawPin places one pin and numbers it in file order, so the number on the
  // pin is the number in the list beside it.
  function drawPin(entry, el, number) {
    var pin = document.createElement("button");
    pin.type = "button";
    pin.className = "relevo-pin";
    pin.setAttribute("data-relevo-id", entry.id);
    pin.textContent = String(number);
    pin.title = entry.text || "";
    if (!entry.selector) pin.className += " relevo-board-pin";
    else if (!el) pin.className += " relevo-orphan-pin";
    // The pin reports its own position with the id: the shell floats a card
    // beside it and cannot see inside this frame to find it again.
    pin.addEventListener("click", function (e) {
      e.preventDefault();
      e.stopPropagation();
      var box = pin.getBoundingClientRect();
      post({
        type: "relevo.pin", id: entry.id,
        x: Math.round(box.left + box.width / 2),
        y: Math.round(box.top + box.height / 2)
      });
    });
    pinLayer.appendChild(pin);
    placePin(pin, entry, el, number);
  }

  // placePin positions a pin from the fraction and the element's live box, so it
  // follows its element through a scroll and a resize.
  function placePin(pin, entry, el, number) {
    if (!el) {
      pin.style.left = "8px";
      pin.style.top = (8 + (number - 1) * 26) + "px";
      return;
    }
    var box = el.getBoundingClientRect();
    pin.style.left = (box.left + box.width * clamp(entry.x)) + "px";
    pin.style.top = (box.top + box.height * clamp(entry.y)) + "px";
  }

  // reposition recomputes every pin against its element's current box.
  function reposition() {
    if (!pinLayer) return;
    for (var i = 0; i < pins.length; i++) {
      var pin = pinLayer.querySelector('[data-relevo-id="' + pins[i].id + '"]');
      if (!pin) continue;
      placePin(pin, pins[i], pins[i].selector ? resolve(pins[i].selector) : null, i + 1);
    }
  }

  // points reports where each pin sits, so the shell can anchor a card beside
  // one, reading the placed pin's own box rather than recomputing it.
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

  // outline keeps exactly one element marked: the last one hovered. Mode-off
  // passes null, which drops the class and leaves the board as found.
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
  // board's own link does not navigate and its handlers never see the click.
  // A click on our own pin is let through: this listener is in the capture
  // phase, so stopping propagation here would make the pin unclickable.
  function onClick(e) {
    var el = e.target;
    if (el && el.closest && el.closest(".relevo-pin")) return;
    e.preventDefault();
    e.stopPropagation();
    pick(e.target, e.clientX, e.clientY);
  }

  // setMode turns comment mode on and off. Off removes the click listener and
  // drops the outline, so the board is left as it was found.
  function setMode(on) {
    mode = on === true;
    if (mode) {
      document.addEventListener("click", onClick, true);
    } else {
      document.removeEventListener("click", onClick, true);
      outline(null);
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", installStyles);
  } else {
    installStyles();
  }
  post({ type: "relevo.overlayReady" });
})();
