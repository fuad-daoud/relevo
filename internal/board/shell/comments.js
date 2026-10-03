// comments.js -- the shell's comment mode: the toggle, the draft box, the list,
// and the banner for a board that moved under an open draft.
//
// This file runs in the shell document, which is the same origin as the API and
// the only place the token lives. It reads the board's notes from the same
// GET /api/board that renders the frame, posts through the token, and treats
// every message from the frame as a claim about what was clicked.
//
// shell.js renders the frame and publishes a small surface -- tokenFromFragment,
// render, load, showBanner -- on window.__relevo so the two files share one
// token read and one board read instead of fetching the board twice.
(function () {
  "use strict";

  var api = window.__relevo;
  if (!api) return;

  var token = api.tokenFromFragment(window.location.hash);
  var mode = false;
  var doc = null;            // the last board document
  var dirty = false;         // a draft is open with unsent text
  var aetag = "";            // the etag a post will name
  var picked = null;         // the selector and position of the pending pick
  var draft = "";            // the pending comment text
  var orphanIds = {};        // ids the frame reported as having no element
  var polling = null;
  var POLL_MS = 2000;

  var commentBtn = document.getElementById("comment-toggle");
  var pane = document.getElementById("comments");
  var listEl = document.getElementById("comment-list");
  var draftEl = document.getElementById("comment-draft");
  var postBtn = document.getElementById("comment-post");
  var selEl = document.getElementById("comment-selector");
  var applyBtn = document.getElementById("banner-apply");
  var bannerEl = document.getElementById("banner");

  // ---- toggle --------------------------------------------------------------

  // setMode turns comment mode on and off. Turning it off takes the listeners
  // back out of the frame and clears the pending pick, so nothing is left
  // half-armed on a board nobody is commenting on.
  function setMode(on) {
    mode = on === true;
    commentBtn.classList.toggle("on", mode);
    commentBtn.setAttribute("aria-pressed", mode ? "true" : "false");
    pane.classList.toggle("open", mode);
    postToFrame({ type: "relevo.mode", on: mode });
    if (!mode) {
      picked = null;
      selEl.textContent = "";
    }
  }

  function postToFrame(msg) {
    var frame = document.getElementById("frame");
    if (frame && frame.contentWindow) frame.contentWindow.postMessage(msg, "*");
  }

  // ---- messages from the frame ---------------------------------------------

  // onFrameMessage accepts a message only when it came from our own frame. The
  // check is ev.source against the frame's contentWindow, not a shape test: a
  // board's own scripts share this frame and can post anything they like, so the
  // source is the only thing that identifies the frame rather than a claim
  // about who is speaking.
  function onFrameMessage(ev) {
    var frame = document.getElementById("frame");
    if (!frame || ev.source !== frame.contentWindow) return;
    var msg = ev.data;
    if (!msg || typeof msg !== "object") return;
    if (msg.type === "relevo.overlayReady") {
      // The frame announces itself only once its overlay script is listening.
      // Anything sent at render time was posted into a document that did not
      // exist yet and was lost, so the mode and the pins are both re-sent here.
      // This is what makes a re-render land the pins without a reload.
      postToFrame({ type: "relevo.mode", on: mode });
      postToFrame({ type: "relevo.pins", annotations: (doc && doc.annotations) || [] });
      return;
    }
    if (msg.type === "relevo.pick") {
      openDraft(msg.selector || "", msg.x, msg.y);
      return;
    }
    if (msg.type === "relevo.orphans") {
      orphanIds = {};
      for (var i = 0; i < (msg.ids || []).length; i++) orphanIds[msg.ids[i]] = true;
      render();
      return;
    }
    if (msg.type === "relevo.pin") focus(msg.id);
  }
  window.addEventListener("message", onFrameMessage);

  // ---- the draft -----------------------------------------------------------

  // openDraft takes a pick and opens the draft box. The selector is shown, not
  // editable: it was built by the overlay from the element the user clicked, and
  // the shell does not second-guess it.
  function openDraft(selector, x, y) {
    picked = { selector: selector, x: x, y: y };
    draft = "";
    draftEl.value = "";
    selEl.textContent = selector ? selector : "board";
    setDirty(true);
    draftEl.focus();
  }

  // setDirty marks the page as having a draft worth keeping, which is what makes
  // the poll show the banner instead of re-rendering the frame under the user.
  function setDirty(on) {
    dirty = on === true;
    pane.classList.toggle("dirty", dirty);
  }

  function post() {
    if (!picked) return;
    var body = JSON.stringify({
      selector: picked.selector,
      x: picked.x,
      y: picked.y,
      text: draftEl.value
    });
    fetch("api/annotations", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Relevo-Board-Token": token,
        "If-Match": aetag
      },
      body: body
    }).then(function (res) {
      if (res.status === 409) {
        // Someone or something appended between the read and this post. The
        // draft is kept and the banner offers the update; nothing is merged.
        setDirty(true);
        showStale();
        return;
      }
      if (!res.ok) throw new Error("the server answered " + res.status);
      return res.json();
    }).then(function (posted) {
      // The post worked. The entry joins the document the list and the pins
      // both read from; there is no refresh to call, only the state to adopt.
      if (!posted) return;
      aetag = posted.annotationsEtag || aetag;
      if (doc) doc.annotations = (doc.annotations || []).concat([posted.annotation]);
      picked = null;
      setDirty(false);
      draftEl.value = "";
      selEl.textContent = "";
      postToFrame({ type: "relevo.pins", annotations: (doc && doc.annotations) || [] });
      render();
    }).catch(function (err) {
      api.setStatus("could not post the comment: " + err.message);
    });
  }

  // ---- the list ------------------------------------------------------------

  // render draws the list in file order: board-level notes under a "board"
  // heading, then element notes, each showing who wrote it, when, and what. An
  // orphan is marked rather than dropped.
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
      var empty = document.createElement("p");
      empty.className = "relevo-empty";
      empty.textContent = "no comments yet";
      listEl.appendChild(empty);
    }
    if (doc && doc.annotationsError) {
      var bad = document.createElement("p");
      bad.className = "relevo-error";
      bad.textContent = "comments could not be read: " + doc.annotationsError;
      listEl.appendChild(bad);
    }
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

    var who = document.createElement("span");
    who.className = "relevo-by";
    who.textContent = entry.by + " · " + entry.at;
    var what = document.createElement("span");
    what.className = "relevo-text";
    what.textContent = entry.text;
    row.appendChild(who);
    row.appendChild(what);
    if (orphanIds[entry.id]) {
      var mark = document.createElement("span");
      mark.className = "relevo-orphan-mark";
      mark.textContent = "element not found";
      row.appendChild(mark);
    }
    row.addEventListener("click", function () { focus(entry.id); });
    listEl.appendChild(row);
  }

  // focus is the two-way link: clicking a pin scrolls the list to its row, and
  // clicking a row scrolls the board to its pin.
  function focus(id) {
    var rows = listEl.querySelectorAll("[data-relevo-id]");
    for (var i = 0; i < rows.length; i++) {
      var hit = rows[i].getAttribute("data-relevo-id") === id;
      rows[i].classList.toggle("focused", hit);
      if (hit && rows[i].scrollIntoView) {
        rows[i].scrollIntoView({ block: "nearest" });
      }
    }
    postToFrame({ type: "relevo.focus", id: id });
  }

  // ---- live updates --------------------------------------------------------

  // startPolling asks the board document every two seconds with the etags it
  // already holds. A 204 means nothing moved and costs nothing; anything else is
  // the whole document again.
  function startPolling() {
    if (polling) return;
    polling = setInterval(poll, POLL_MS);
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden) poll();
    });
  }

  function poll() {
    if (document.hidden) return;
    var board = (doc && doc.etag) || "";
    fetch("api/board?etag=" + encodeURIComponent(board) +
      "&aetag=" + encodeURIComponent(aetag), {
      headers: { "X-Relevo-Board-Token": token }
    }).then(function (res) {
      if (res.status === 204) return null;
      if (!res.ok) throw new Error("the server answered " + res.status);
      return res.json();
    }).then(function (next) {
      if (!next) return;
      onBoardDocument(next);
    }).catch(function () {
      // A failed poll is not worth a banner: the next one is two seconds away.
    });
  }

  // onBoardDocument applies a fresh document, or holds it back. A clean page
  // takes the update straight away. A page with a draft open is told, and keeps
  // what it was writing -- nothing is ever merged into a draft.
  function onBoardDocument(next) {
    if (!dirty) {
      adopt(next);
      return;
    }
    pending = next;
    showStale();
  }

  var pending = null;

  // adopt installs a document as the current one and draws it.
  //
  // The frame is re-rendered only when the board itself moved. A new annotations
  // etag redraws the pins and nothing else: re-rendering the frame on every
  // comment would tear down and rebuild the whole document -- losing scroll
  // position, the page's own state, and the click that is in flight -- to show a
  // change that is only pins.
  function adopt(next) {
    var boardMoved = !doc || next.isNew || (next.html || "") !== (doc.html || "");
    doc = next;
    aetag = next.annotationsEtag || "";
    if (boardMoved) api.render(next.html);
    postToFrame({ type: "relevo.pins", annotations: next.annotations || [] });
    render();
    if (next.external && next.external.length) {
      api.showBanner(
        "this board references " + next.external.length +
          " external resource(s), which the board's content security policy blocks: " +
          next.external.join(", ")
      );
    }
  }

  // showStale is the banner: it names the change and offers the update without
  // taking it. The draft underneath is untouched.
  function showStale() {
    bannerEl.textContent =
      "the board changed on disk -- your draft is kept; apply to take the update";
    bannerEl.className = "on";
    applyBtn.style.display = "inline-block";
  }

  // apply takes the held-back document and clears the banner. The draft stays:
  // applying the board is not applying the comment.
  function apply() {
    applyBtn.style.display = "none";
    bannerEl.className = "";
    bannerEl.textContent = "";
    if (!pending) return;
    var held = pending;
    pending = null;
    adopt(held);
  }

  // ---- wiring --------------------------------------------------------------

  commentBtn.addEventListener("click", function () { setMode(!mode); });
  postBtn.addEventListener("click", post);
  applyBtn.addEventListener("click", apply);
  draftEl.addEventListener("input", function () { draft = draftEl.value; });

  // boardDoc is published by shell.js once it has read the board, so the two
  // files share one read and one token rather than each fetching.
  function boot() {
    if (api.onDocument) api.onDocument(function (next) {
      doc = next;
      aetag = next.annotationsEtag || "";
      postToFrame({ type: "relevo.pins", annotations: next.annotations || [] });
      render();
      startPolling();
    });
  }
  boot();

  window.__relevoComments = { setMode: setMode, focus: focus };
})();
