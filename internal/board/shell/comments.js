// comments.js -- the shell's comment mode: the toggle, the floating panes, the
// input dialog a pick opens, and the banner for a board that moved under an open
// draft. It runs in the shell document, the same origin as the API and the only
// place the token lives, and treats every message from the frame as a claim.
(function () {
  "use strict";

  var api = window.__relevo;
  var list = window.__relevoList;
  if (!api || !list) return;

  var token = api.tokenFromFragment(window.location.hash);
  var mode = false;
  var doc = null;            // the last board document
  var dirty = false;         // a draft is open with unsent text
  var aetag = "";            // the etag a post will name
  var picked = null;         // the selector and position of the pending pick
  var draft = "";            // the pending comment text
  var pending = null;        // a document held back by an open draft
  var polling = null;
  var POLL_MS = 2000;

  var commentBtn = document.getElementById("comment-toggle");
  var pane = document.getElementById("comments");
  var closeBtn = document.getElementById("comments-close");
  var postBtn = document.getElementById("comment-post");
  var draftEl = document.getElementById("comment-draft");
  var selEl = document.getElementById("comment-selector");
  var applyBtn = document.getElementById("banner-apply");
  var bannerEl = document.getElementById("banner");
  var dialogEl = document.getElementById("comment-dialog");
  var cancelBtn = document.getElementById("comment-dialog-close");

  // setMode turns comment mode on and off. Off takes the listeners back out of the
  // frame, closes the card, drops the dots and puts the composer away, so nothing
  // is left half-armed on a board nobody is commenting on.
  function setMode(on) {
    mode = on === true;
    commentBtn.classList.toggle("on", mode);
    commentBtn.setAttribute("aria-pressed", mode ? "true" : "false");
    pane.classList.toggle("open", mode);
    postToFrame({ type: "relevo.mode", on: mode });
    if (mode) {
      postPins();
      return;
    }
    list.closeCard();
    closeDialog();
    postToFrame({ type: "relevo.pins", annotations: [] });
    // Focus returns to the toggle rather than to a box in a pane that is gone.
    commentBtn.focus();
  }

  function postToFrame(msg) {
    var frame = document.getElementById("frame");
    if (frame && frame.contentWindow) frame.contentWindow.postMessage(msg, "*");
  }

  // postPins sends the annotations only in comment mode: out of it the board has none.
  function postPins() {
    if (!mode) return;
    postToFrame({ type: "relevo.pins", annotations: (doc && doc.annotations) || [] });
  }

  // requestPoints asks the frame where it drew the dots: only the frame can see them.
  function requestPoints() {
    if (!mode) return;
    postToFrame({ type: "relevo.points" });
  }

  // onFrameMessage accepts a message only when it came from our own frame: the
  // board's own scripts share that frame, so the source is the only identity.
  function onFrameMessage(ev) {
    var frame = document.getElementById("frame");
    if (!frame || ev.source !== frame.contentWindow) return;
    var msg = ev.data;
    if (!msg || typeof msg !== "object") return;
    if (msg.type === "relevo.overlayReady") {
      // The frame announces itself only once its overlay is listening, so
      // anything sent at render time went into a document that did not exist yet.
      postToFrame({ type: "relevo.mode", on: mode });
      postPins();
      return;
    }
    if (msg.type === "relevo.pick") { openDraft(msg.selector || "", msg.x, msg.y); return; }
    if (msg.type === "relevo.orphans") { list.setOrphans(msg.ids); return; }
    if (msg.type === "relevo.points") { list.setPoints(msg.points); return; }
    if (msg.type === "relevo.pin") {
      // A dot was clicked: open that thread's card and mark its list row.
      list.setPoints([]);
      list.openCard(msg.id, msg.x, msg.y);
    }
  }
  window.addEventListener("message", onFrameMessage);

  // openDraft takes a pick and opens the composer on it, which is what a fresh click
  // on the board opens. The selector is shown, not edited: the overlay built it.
  function openDraft(selector, x, y) {
    picked = { selector: selector, x: x, y: y };
    draft = "";
    draftEl.value = "";
    selEl.textContent = selector ? selector : "board";
    setDirty(true);
    dialogEl.classList.add("open");
    draftEl.focus();
  }

  // closeDialog puts the composer away without posting. The pick goes with it, so a
  // Post after it has nothing to name: the dialog is closed, not merely emptied.
  function closeDialog() {
    picked = null;
    draft = "";
    draftEl.value = "";
    selEl.textContent = "";
    setDirty(false);
    dialogEl.classList.remove("open");
  }

  function setDirty(on) {
    dirty = on === true;
    pane.classList.toggle("dirty", dirty);
  }

  function post(anchor, text) {
    if (!anchor) return;
    var body = JSON.stringify({
      selector: anchor.selector,
      x: anchor.x,
      y: anchor.y,
      text: text
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
        // Something appended between the read and this post: the draft is kept.
        setDirty(true);
        showStale();
        return;
      }
      if (!res.ok) throw new Error("the server answered " + res.status);
      return res.json();
    }).then(function (posted) {
      // The post worked: the entry joins the document the dots and the list read.
      if (!posted) return;
      aetag = posted.annotationsEtag || aetag;
      if (doc) doc.annotations = (doc.annotations || []).concat([posted.annotation]);
      setDirty(false);
      // Only the box this post came out of is emptied: a reply leaves the open card
      // alone, and a first post puts the composer away.
      if (picked === anchor) closeDialog(); else list.clearReply();
      postPins();
      list.setDocument(doc);
    }).catch(function (err) {
      api.setStatus("could not post the comment: " + err.message);
    });
  }

  // startPolling asks for the board every two seconds with the etags held.
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
    }).catch(function () {});   // a failed poll is not worth a banner: another is two seconds away
  }

  function onBoardDocument(next) {
    if (!dirty) {
      adopt(next);
      return;
    }
    pending = next;
    showStale();
  }

  // adopt installs a document as the current one and draws it. The frame is
  // re-rendered only when the board itself moved: a new annotations etag redraws
  // the dots and nothing else, because re-rendering on every comment tears down
  // the whole document -- scroll position, page state, the click in flight.
  function adopt(next) {
    var boardMoved = !doc || next.isNew || (next.html || "") !== (doc.html || "");
    doc = next;
    aetag = next.annotationsEtag || "";
    if (boardMoved) api.render(next.html);
    postPins();
    list.setDocument(doc);
    requestPoints();
    if (next.external && next.external.length) {
      api.showBanner(
        "this board references " + next.external.length +
          " external resource(s), which the board's content security policy blocks: " +
          next.external.join(", ")
      );
    }
  }

  // showStale names the change and offers the update without taking it.
  function showStale() {
    bannerEl.textContent =
      "the board changed on disk -- your draft is kept; apply to take the update";
    bannerEl.className = "on";
    applyBtn.style.display = "inline-block";
  }

  // apply takes the held-back document and clears the banner. Applying the board is not applying the comment.
  function apply() {
    applyBtn.style.display = "none";
    bannerEl.className = "";
    bannerEl.textContent = "";
    if (!pending) return;
    var held = pending;
    pending = null;
    adopt(held);
  }

  // submitDraft is the one path a draft takes to the API: Post and Ctrl+Enter
  // both call it, so a keypress and a click cannot post differently.
  function submitDraft() {
    post(picked, draftEl.value);
  }

  commentBtn.addEventListener("click", function () { setMode(!mode); });
  closeBtn.addEventListener("click", function () { setMode(false); });
  postBtn.addEventListener("click", submitDraft);
  cancelBtn.addEventListener("click", closeDialog);
  applyBtn.addEventListener("click", apply);
  draftEl.addEventListener("input", function () { draft = draftEl.value; });
  list.submitOnCtrlEnter(draftEl, submitDraft);

  // The card's reply box posts here too, onto the anchor of the thread it answers.
  list.setReplyHandler(post);

  // Escape unwinds one layer at a time: the card, then the composer, then the panel.
  document.addEventListener("keydown", function (ev) {
    if (ev.key !== "Escape") return;
    if (list.cardOpen()) {
      list.closeCard();
      commentBtn.focus();
      return;
    }
    if (dialogEl.classList.contains("open")) {
      closeDialog();
      return;
    }
    if (mode) setMode(false);
  });

  // A click on the canvas is a pick, not a dismissal: the frame owns that gesture.
  document.addEventListener("click", function (ev) {
    if (!mode) return;
    var frame = document.getElementById("frame");
    if (frame && frame.contains(ev.target)) return;
    if (pane.contains(ev.target)) return;
    if (dialogEl.contains(ev.target)) return;
    // The toggle opened the panel with this same click as it bubbles up.
    if (commentBtn.contains(ev.target)) return;
    if (list.cardOpen()) {
      list.closeCard();
      return;
    }
    setMode(false);
  });

  function boot() {
    if (api.onDocument) api.onDocument(function (next) {
      doc = next;
      aetag = next.annotationsEtag || "";
      postPins();
      list.setDocument(doc);
      requestPoints();
      startPolling();
    });
  }
  boot();

  window.__relevoComments = { setMode: setMode, focus: list.focus };
})();
