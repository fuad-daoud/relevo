// commentlist.js -- drawing the comments: the all-comments list, and the one
// floating card that shows a single thread beside its dot. It is the rendering
// half of comment mode: it holds no token and writes nothing, and it reads the
// board document comments.js already read rather than fetching a second copy.
(function () {
  "use strict";

  var doc = null;            // the last board document
  var orphanIds = {};        // ids the frame reported as having no element
  var pinPoints = {};        // id -> where the frame last drew that pin
  var labels = {};           // selector -> what the frame calls that anchor
  var onReply = null;        // comments.js posts the card's reply box
  var onOpenCard = null;    // comments.js closes the composer when a card opens
  var HINT = "Ctrl+Enter to post";

  var listEl = document.getElementById("comment-list");
  var cardEl = document.getElementById("thread-card");
  var cardId = null;         // the thread the open card is showing
  var cardLabel = null;      // the card's own heading, rewritten when a label arrives

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

  // setPoints records where the frame drew each dot, the only place the card can anchor from.
  function setPoints(points) {
    pinPoints = {};
    for (var i = 0; i < (points || []).length; i++) {
      pinPoints[points[i].id] = points[i];
    }
    if (cardId) placeCard();
  }

  // setLabels records what the frame calls each anchor, so a card can head itself with a
  // name instead of a CSS path. The open card is rewritten in place rather than drawn
  // again: a label turning up must not empty a reply already being typed into it.
  function setLabels(next) {
    labels = {};
    for (var i = 0; i < (next || []).length; i++) {
      labels[next[i].selector] = next[i].label;
    }
    if (cardId && cardLabel) {
      var thread = threadById(cardId);
      if (thread) cardLabel.textContent = labelFor(thread);
    }
  }

  // labelFor is a thread's anchor by name: the board's own id for it where the frame found
  // one, otherwise the element's tag with a few words of its own text. An anchor with no
  // name yet says what it is rather than falling back to the selector, which is the
  // agent's and the CLI's to read.
  function labelFor(thread) {
    if (!thread.key) return "the board";
    return labels[thread.key] || "element";
  }

  // threads groups the flat annotations by their anchor, in file order: the same
  // selector string is one thread, an empty selector the board. Presentation only,
  // so the stored file stays flat; a thread's handle is its first entry's id.
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
      listEl.appendChild(line("h3", "", "board"));
      for (var b = 0; b < boardLevel.length; b++) addRow(boardLevel[b]);
    }
    if (anchored.length) {
      listEl.appendChild(line("h3", "", "elements"));
      for (var a = 0; a < anchored.length; a++) addRow(anchored[a]);
    }
    if (!all.length) {
      listEl.appendChild(line("p", "relevo-empty", "no comments yet"));
    }
    if (doc && doc.annotationsError) {
      listEl.appendChild(line("p", "relevo-error", "comments could not be read: " + doc.annotationsError));
    }
    if (cardId && !threadById(cardId)) closeCard();
  }

  // line is every piece of text the panel and the card are built from: one tag,
  // one class, the text. A row's lines, an orphan mark and a heading are all it.
  function line(tag, className, text) {
    var el = document.createElement(tag);
    el.className = className || "";
    el.textContent = text;
    return el;
  }

  // byLine is the "who, when" line a row and the card share.
  function byLine(entry) {
    return line("span", "relevo-by", entry.by + " · " + entry.at);
  }

  function orphanMark() {
    return line("span", "relevo-orphan-mark", "element not found");
  }

  // addRow is one thread rather than one note: the row carries the first note on
  // that anchor, and opens the card on the whole conversation.
  function addRow(thread) {
    var first = thread.entries[0];
    var row = document.createElement("button");
    row.type = "button";
    row.className = "relevo-row";
    row.setAttribute("data-relevo-id", thread.id);
    if (orphan(thread)) row.className += " orphan";
    row.appendChild(byLine(first));
    row.appendChild(line("span", "relevo-text", first.text));
    // The count is in the by-line's aside style: the row is a conversation, and
    // the number is the only thing on it that says so.
    if (thread.entries.length > 1) {
      row.appendChild(line("span", "relevo-by", thread.entries.length + " in this thread"));
    }
    if (orphan(thread)) row.appendChild(orphanMark());
    row.addEventListener("click", function () { openCard(thread.id); });
    listEl.appendChild(row);
  }

  // focus marks the row alone: opening every thread would leave the board unseen.
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

  // openCard shows one thread beside its dot: what it is about, every note on that anchor
  // in file order, then the box that answers them and the row of controls under it. One
  // card at a time, and re-opening the open one is how a second click closes it.
  // onOpenCard is how comments.js keeps the composer out of the way: the card is
  // the one writing surface while it is open, wherever the reader got to it from.
  function openCard(id, x, y) {
    if (cardId === id) {
      closeCard();
      return;
    }
    var thread = threadById(id);
    if (!thread) return;
    if (onOpenCard) onOpenCard();
    cardId = id;
    var point = pinPoints[id] || (x != null ? { x: x, y: y } : null);
    cardEl.textContent = "";
    cardLabel = line("div", "relevo-anchor", labelFor(thread));
    cardEl.appendChild(cardLabel);
    for (var i = 0; i < thread.entries.length; i++) {
      var entry = thread.entries[i];
      cardEl.appendChild(byLine(entry));
      cardEl.appendChild(line("span", "relevo-text", entry.text));
      if (orphanIds[entry.id]) cardEl.appendChild(orphanMark());
    }
    cardEl.appendChild(replyBox(thread));
    cardEl.classList.add("open");
    placeCard(point);
    focus(id);
  }

  // replyBox is the card's bottom section: the box that answers the thread, and
  // the row of controls under it -- the keyboard hint left, the way out right,
  // a row rather than a control floated beside the box, which the box then
  // covered. It posts through the handler comments.js gave the thread's anchor:
  // a reply lands on the thread, not on the last pick.
  function replyBox(thread) {
    var box = document.createElement("textarea");
    box.id = "thread-reply";
    box.placeholder = "reply to this thread";
    submitOnCtrlEnter(box, function () { sendReply(thread, box); });
    var row = document.createElement("div");
    row.className = "relevo-card-actions";
    row.appendChild(line("span", "relevo-hint", HINT));
    row.appendChild(closeButton());
    var wrap = document.createElement("div");
    wrap.appendChild(box);
    wrap.appendChild(row);
    return wrap;
  }

  // sendReply is the one post a reply makes: Ctrl+Enter is the only path to it.
  function sendReply(thread, box) {
    var text = box.value.trim();
    if (text && onReply) onReply(anchorOf(thread), text);
  }

  // submitOnCtrlEnter runs fn on Ctrl+Enter, so every box posts from the keyboard
  // through its button's path. Enter alone stays a newline: comments are prose.
  function submitOnCtrlEnter(el, fn) {
    el.addEventListener("keydown", function (ev) {
      if (!ev.ctrlKey || ev.key !== "Enter") return;
      ev.preventDefault();
      fn();
    });
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

  // setCardOpenHandler is the other half of that seam: this file holds the card,
  // comments.js holds the composer, and only comments.js may put one away.
  function setCardOpenHandler(fn) {
    onOpenCard = fn;
  }

  // clearReply empties the box once a reply has landed, by drawing the card again
  // with the entry just posted: the thread on screen has grown, the box is empty
  // and the card stays where the reader left it. A post never tears the card
  // down. Dropping the handle first makes the redraw an open, not the toggle.
  function clearReply() {
    if (!cardId) return;
    var id = cardId;
    cardId = null;
    openCard(id);
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
    cardLabel = null;
    cardEl.className = "";
    cardEl.textContent = "";
  }

  function cardOpen() {
    return cardId !== null;
  }

  // placeCard puts the card beside its dot, flipping side near an edge; the dot's
  // coordinates are frame-relative, so the frame's own position carries them.
  function placeCard(point) {
    placeBeside(cardEl, point);
  }

  // placeBeside is the one placement both floating surfaces use, so the thread card and
  // the composer a pick opens cannot disagree about where an edge has room. A point with
  // no coordinates places nothing and leaves the box where its own rule puts it, which is
  // the corner rather than off-screen.
  function placeBeside(el, point) {
    if (!el || !point || typeof point.x !== "number" || typeof point.y !== "number") return;
    var frame = document.getElementById("frame");
    var frameBox = frame && frame.getBoundingClientRect ? frame.getBoundingClientRect() : null;
    var px = (frameBox ? frameBox.left : 0) + point.x;
    var py = (frameBox ? frameBox.top : 0) + point.y;
    var box = el.getBoundingClientRect();
    var wide = box.width || 288;
    var tall = box.height || 72;
    var left = px + 14;
    if (left + wide > window.innerWidth - 8) left = px - wide - 14;
    if (left < 8) left = 8;
    var top = py - tall / 2;
    if (top < 8) top = 8;
    if (top + tall > window.innerHeight - 8) top = window.innerHeight - tall - 8;
    el.style.left = left + "px";
    el.style.top = Math.max(8, top) + "px";
  }

  window.__relevoList = {
    setDocument: setDocument,
    setOrphans: setOrphans,
    setPoints: setPoints,
    setLabels: setLabels,
    render: render,
    focus: focus,
    openCard: openCard,
    placeBeside: placeBeside,
    closeCard: closeCard,
    cardOpen: cardOpen,
    setReplyHandler: setReplyHandler,
    setCardOpenHandler: setCardOpenHandler,
    clearReply: clearReply,
    submitOnCtrlEnter: submitOnCtrlEnter
  };
})();