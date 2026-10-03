// commentlist.js -- drawing the comments: the all-comments list, and the one
// floating card that shows a single thread beside its pin.
//
// This is the rendering half of comment mode, split out of comments.js so each
// file stays readable on its own. It holds no token and writes nothing: posting
// a note is comments.js's job alone. It reads the same board document off the
// same small window.__relevo surface rather than fetching a second copy.
(function () {
  "use strict";

  var doc = null;            // the last board document
  var orphanIds = {};        // ids the frame reported as having no element
  var pinPoints = {};        // id -> where the frame last drew that pin
  var onReply = null;        // comments.js posts the card's reply box

  var listEl = document.getElementById("comment-list");
  var cardEl = document.getElementById("thread-card");
  var cardId = null;         // the thread the open card is showing

  // ---- the document ---------------------------------------------------------

  // setDocument redraws, so the card is placed against the pins on screen now.
  function setDocument(next) {
    doc = next;
    render();
  }

  function setOrphans(ids) {
    orphanIds = {};
    for (var i = 0; i < (ids || []).length; i++) orphanIds[ids[i]] = true;
    render();
  }

  // setPoints records where the frame drew each pin; the card is anchored from
  // these rather than from the pin's DOM, which the shell cannot read.
  function setPoints(points) {
    pinPoints = {};
    for (var i = 0; i < (points || []).length; i++) {
      pinPoints[points[i].id] = points[i];
    }
    if (cardId) placeCard();
  }

  // threads groups the document's flat annotations by their anchor, in file
  // order: the same selector string is one thread, an empty selector the board.
  // Presentation only -- the stored file stays flat -- and a thread carries the
  // first entry's id as the handle the pins, rows and card share.
  function threads() {
    var entries = (doc && doc.annotations) || [];
    var out = [];
    var by = {};
    for (var i = 0; i < entries.length; i++) {
      var entry = entries[i];
      var key = "$" + (entry.selector || "");
      var thread = by[key];
      if (!thread) {
        thread = by[key] = { key: entry.selector || "", id: entry.id, entries: [] };
        out.push(thread);
      }
      thread.entries.push(entry);
    }
    return out;
  }

  // threadById resolves a handle to its thread, so pin, row and card name one thread.
  function threadById(id) {
    var all = threads();
    for (var i = 0; i < all.length; i++) {
      if (all[i].id === id) return all[i];
    }
    return null;
  }

  // orphan reports whether an anchor no longer resolves: every note on it shares that.
  function orphan(thread) {
    for (var i = 0; i < thread.entries.length; i++) {
      if (orphanIds[thread.entries[i].id]) return true;
    }
    return false;
  }

  // ---- the list ------------------------------------------------------------

  // render draws one row per thread, board-level notes first.
  function render() {
    listEl.textContent = "";
    var all = threads();
    var boardLevel = [];
    var anchored = [];
    for (var i = 0; i < all.length; i++) {
      (all[i].key ? anchored : boardLevel).push(all[i]);
    }
    if (boardLevel.length) {
      listEl.appendChild(heading("board"));
      for (var b = 0; b < boardLevel.length; b++) addRow(boardLevel[b]);
    }
    if (anchored.length) {
      listEl.appendChild(heading("elements"));
      for (var a = 0; a < anchored.length; a++) addRow(anchored[a]);
    }
    if (!all.length) {
      listEl.appendChild(note("relevo-empty", "no comments yet"));
    }
    if (doc && doc.annotationsError) {
      listEl.appendChild(note("relevo-error", "comments could not be read: " + doc.annotationsError));
    }
    if (cardId && !threadById(cardId)) closeCard();
  }

  function note(className, text) {
    var el = document.createElement("p");
    el.className = className;
    el.textContent = text;
    return el;
  }

  function heading(text) {
    var h = document.createElement("h3");
    h.textContent = text;
    return h;
  }

  // addRow is one thread rather than one note: the row carries the first note on
  // that anchor, and opens the card with the whole conversation.
  function addRow(thread) {
    var first = thread.entries[0];
    var row = document.createElement("button");
    row.type = "button";
    row.className = "relevo-row";
    row.setAttribute("data-relevo-id", thread.id);
    if (orphan(thread)) row.className += " orphan";
    row.appendChild(byLine(first));
    row.appendChild(textLine(first.text));
    if (thread.entries.length > 1) row.appendChild(countLine(thread.entries.length));
    if (orphan(thread)) row.appendChild(orphanMark());
    row.addEventListener("click", function () { openCard(thread.id); });
    listEl.appendChild(row);
  }

  // countLine says the row is a conversation, in the by-line's aside style.
  function countLine(n) {
    var el = document.createElement("span");
    el.className = "relevo-by";
    el.textContent = n + " in this thread";
    return el;
  }

  // byLine is the "who, when" line, shared by a row and by the card.
  function byLine(entry) {
    var who = document.createElement("span");
    who.className = "relevo-by";
    who.textContent = entry.by + " · " + entry.at;
    return who;
  }

  function textLine(text) {
    var what = document.createElement("span");
    what.className = "relevo-text";
    what.textContent = text;
    return what;
  }

  function orphanMark() {
    var mark = document.createElement("span");
    mark.className = "relevo-orphan-mark";
    mark.textContent = "element not found";
    return mark;
  }

  // focus marks the row without opening the card: opening every thread from the
  // list would leave the board unseen.
  function focus(id) {
    var rows = listEl.querySelectorAll("[data-relevo-id]");
    for (var i = 0; i < rows.length; i++) {
      var hit = rows[i].getAttribute("data-relevo-id") === id;
      rows[i].classList.toggle("focused", hit);
      if (hit && rows[i].scrollIntoView) {
        rows[i].scrollIntoView({ block: "nearest" });
      }
    }
  }

  // ---- the floating card ---------------------------------------------------

  // openCard shows one thread beside its pin: every note on that anchor, in file
  // order, and the reply box that answers them. One card at a time is the point,
  // and re-opening the card already showing is how a second click closes it.
  function openCard(id, x, y) {
    if (cardId === id) {
      closeCard();
      return;
    }
    var thread = threadById(id);
    if (!thread) return;
    cardId = id;
    var point = pinPoints[id] || (x != null ? { x: x, y: y } : null);
    cardEl.textContent = "";
    for (var i = 0; i < thread.entries.length; i++) {
      var entry = thread.entries[i];
      cardEl.appendChild(byLine(entry));
      cardEl.appendChild(textLine(entry.text));
      if (orphanIds[entry.id]) cardEl.appendChild(orphanMark());
    }
    cardEl.appendChild(replyBox(thread));
    cardEl.appendChild(closeButton());
    cardEl.classList.add("open");
    placeCard(point);
    focus(id);
  }

  // replyBox is the card's own draft box, posting through the handler
  // comments.js installed with the thread's anchor, so a reply lands on the
  // element the thread is about rather than on whatever was last clicked.
  function replyBox(thread) {
    var wrap = document.createElement("div");
    var box = document.createElement("textarea");
    box.id = "thread-reply";
    box.placeholder = "reply to this thread";
    var btn = document.createElement("button");
    btn.type = "button";
    btn.textContent = "Reply";
    btn.addEventListener("click", function () {
      var text = box.value.trim();
      if (text && onReply) onReply(anchorOf(thread), text);
    });
    wrap.appendChild(box);
    wrap.appendChild(btn);
    return wrap;
  }

  // anchorOf is where a reply goes: the thread's selector, at the first note's fractions.
  function anchorOf(thread) {
    var first = thread.entries[0];
    return { selector: thread.key, x: first.x, y: first.y };
  }

  // setReplyHandler is how comments.js takes the posting back from this file.
  function setReplyHandler(fn) {
    onReply = fn;
  }

  // clearReply empties the card's box once a reply has landed, if it is still open.
  function clearReply() {
    var box = document.getElementById("thread-reply");
    if (box) box.value = "";
  }

  function closeButton() {
    var btn = document.createElement("button");
    btn.id = "thread-close";
    btn.type = "button";
    btn.setAttribute("aria-label", "close this comment");
    btn.textContent = "×";
    btn.addEventListener("click", closeCard);
    return btn;
  }

  function closeCard() {
    cardId = null;
    cardEl.className = "";
    cardEl.textContent = "";
  }

  function cardOpen() {
    return cardId !== null;
  }

  // placeCard puts the card beside its pin, flipping side near an edge. The pin's
  // coordinates are frame-relative, so they carry the frame's position.
  function placeCard(point) {
    if (!point) return;
    var frame = document.getElementById("frame");
    var frameBox = frame && frame.getBoundingClientRect ? frame.getBoundingClientRect() : null;
    var px = (frameBox ? frameBox.left : 0) + point.x;
    var py = (frameBox ? frameBox.top : 0) + point.y;
    var card = cardEl.getBoundingClientRect();
    var wide = card.width || 288;
    var tall = card.height || 72;
    var left = px + 14;
    if (left + wide > window.innerWidth - 8) left = px - wide - 14;
    if (left < 8) left = 8;
    var top = py - tall / 2;
    if (top < 8) top = 8;
    if (top + tall > window.innerHeight - 8) top = window.innerHeight - tall - 8;
    cardEl.style.left = left + "px";
    cardEl.style.top = Math.max(8, top) + "px";
  }

  window.__relevoList = {
    setDocument: setDocument,
    setOrphans: setOrphans,
    setPoints: setPoints,
    render: render,
    focus: focus,
    openCard: openCard,
    closeCard: closeCard,
    cardOpen: cardOpen,
    setReplyHandler: setReplyHandler,
    clearReply: clearReply
  };
})();