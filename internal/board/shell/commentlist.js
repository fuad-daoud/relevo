// commentlist.js -- drawing the comments: the all-comments list, and the one
// floating card that shows a single thread beside its pin.
//
// This is the rendering half of comment mode, split out of comments.js so each
// file stays readable on its own. It holds no token and posts nothing that
// writes: the only message out of here is a focus request, which asks the
// overlay to scroll a pin into view. Posting a note is comments.js's job alone.
//
// It runs in the shell document alongside comments.js, so it reads the same
// board document off the same small window.__relevo surface rather than
// fetching a second copy. render is called by comments.js whenever the
// document, the orphan set, or the set of pins changes.
(function () {
  "use strict";

  var doc = null;            // the last board document
  var orphanIds = {};        // ids the frame reported as having no element
  var pinPoints = {};        // id -> where the frame last drew that pin

  var listEl = document.getElementById("comment-list");
  var cardEl = document.getElementById("thread-card");
  var cardId = null;         // the thread the open card is showing

  // ---- the document ---------------------------------------------------------

  // setDocument takes a freshly adopted board document. It redraws rather than
  // reading the module-level copy, so the card is placed against the pins that
  // are actually on screen now.
  function setDocument(next) {
    doc = next;
    render();
  }

  function setOrphans(ids) {
    orphanIds = {};
    for (var i = 0; i < (ids || []).length; i++) orphanIds[ids[i]] = true;
    render();
  }

  // setPoints records where the frame drew each pin, in frame coordinates. The
  // card is anchored from these rather than from the pin's DOM, which the shell
  // cannot read across the sandbox boundary.
  function setPoints(points) {
    pinPoints = {};
    for (var i = 0; i < (points || []).length; i++) {
      pinPoints[points[i].id] = points[i];
    }
    if (cardId) placeCard();
  }

  function entryById(id) {
    var entries = (doc && doc.annotations) || [];
    for (var i = 0; i < entries.length; i++) {
      if (entries[i].id === id) return entries[i];
    }
    return null;
  }

  // ---- the list ------------------------------------------------------------

  // render draws the list in file order: board-level notes under a "board"
  // heading, then element notes. An orphan is marked rather than dropped.
  function render() {
    listEl.textContent = "";
    var entries = (doc && doc.annotations) || [];
    var boardLevel = [];
    var anchored = [];
    for (var i = 0; i < entries.length; i++) {
      (entries[i].selector ? anchored : boardLevel).push({ entry: entries[i], n: i + 1 });
    }
    if (boardLevel.length) {
      listEl.appendChild(heading("board"));
      for (var b = 0; b < boardLevel.length; b++) addRow(boardLevel[b]);
    }
    if (anchored.length) {
      listEl.appendChild(heading("elements"));
      for (var a = 0; a < anchored.length; a++) addRow(anchored[a]);
    }
    if (!entries.length) {
      listEl.appendChild(note("relevo-empty", "no comments yet"));
    }
    if (doc && doc.annotationsError) {
      listEl.appendChild(note("relevo-error", "comments could not be read: " + doc.annotationsError));
    }
    if (cardId && !entryById(cardId)) closeCard();
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

  // addRow is one list row. Its number matches the number drawn on the pin, and
  // clicking either one focuses the other.
  function addRow(item) {
    var entry = item.entry;
    var row = document.createElement("button");
    row.type = "button";
    row.className = "relevo-row";
    row.setAttribute("data-relevo-id", entry.id);
    if (orphanIds[entry.id]) row.className += " orphan";
    row.appendChild(byLine(entry));
    row.appendChild(textLine(entry.text));
    if (orphanIds[entry.id]) row.appendChild(orphanMark());
    row.addEventListener("click", function () { openCard(entry.id); });
    listEl.appendChild(row);
  }

  // byLine is the "who, when" line, shared by a row and by the card so the two
  // read identically.
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

  // focus marks the matching row. It does not open the card: a reader who opens
  // every thread by clicking the list would never be able to see the board.
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

  // openCard shows one thread beside its pin. One card at a time is the point:
  // a board can carry dozens of notes, and two cards on screen at once would
  // say which of them the reader was meant to be reading.
  //
  // Re-opening the card already showing is how a second click closes it, so the
  // toggle does not need a second control of its own.
  function openCard(id, x, y) {
    if (cardId === id) {
      closeCard();
      return;
    }
    var entry = entryById(id);
    if (!entry) return;
    cardId = id;
    var point = pinPoints[id] || (x != null ? { x: x, y: y } : null);
    cardEl.textContent = "";
    cardEl.appendChild(byLine(entry));
    cardEl.appendChild(textLine(entry.text));
    if (orphanIds[id]) cardEl.appendChild(orphanMark());
    cardEl.appendChild(closeButton());
    cardEl.classList.add("open");
    placeCard(point);
    focus(id);
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

  // placeCard puts the card beside its pin, flipping to the other side when the
  // point is near an edge. The pin's coordinates are frame-relative, so they are
  // offset by the frame's own position in the shell page.
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
    cardOpen: cardOpen
  };
})();