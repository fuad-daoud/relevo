// comments.js -- the shell's comment mode: the toggle, the floating panes, the
// draft box, and the banner for a board that moved under an open draft.
//
// This file runs in the shell document, the same origin as the API and the only
// place the token lives. It posts through the token and treats every message
// from the frame as a claim about what was clicked. The drawing lives in
// commentlist.js, loaded before this file, which publishes window.__relevoList;
// this file owns everything that writes. shell.js publishes tokenFromFragment,
// render, load and showBanner on window.__relevo, so the files share one token
// read and one board read.
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
  var applyBtn = document.getElementById("banner-apply");
  var bannerEl = document.getElementById("banner");

  // setMode turns comment mode on and off. Off takes the listeners back out of
  // the frame, closes the card and clears the pending pick, so nothing is left
  // half-armed on a board nobody is commenting on.
  function setMode(on) {
    mode = on === true;
    commentBtn.classList.toggle("on", mode);
    commentBtn.setAttribute("aria-pressed", mode ? "true" : "false");
    pane.classList.toggle("open", mode);
    postToFrame({ type: "relevo.mode", on: mode });
    if (mode) {
      draftEl.focus();
      return;
    }
    list.closeCard();
    picked = null;
    selEl.textContent = "";
    // Focus returns to the toggle rather than staying on a button inside a pane
    // that is no longer there.
    commentBtn.focus();
  }

  function postToFrame(msg) {
    var frame = document.getElementById("frame");
    if (frame && frame.contentWindow) frame.contentWindow.postMessage(msg, "*");
  }

  // requestPoints asks the frame where it drew the pins. A card opened from the
  // list has no coordinate of its own, and only the frame can see its pins.
  function requestPoints() {
    postToFrame({ type: "relevo.points" });
  }

  // onFrameMessage accepts a message only when it came from our own frame. The
  // check is ev.source against the frame's contentWindow, not a shape test: the
  // board's own scripts share this frame, so the source is the only
  // identification there is.
  function onFrameMessage(ev) {
    var frame = document.getElementById("frame");
    if (!frame || ev.source !== frame.contentWindow) return;
    var msg = ev.data;
    if (!msg || typeof msg !== "object") return;
    if (msg.type === "relevo.overlayReady") {
      // The frame announces itself only once its overlay is listening. Anything
      // sent at render time was posted into a document that did not exist yet
      // and was lost, so mode and pins are both re-sent here. This is what makes
      // a re-render land the pins without a reload.
      postToFrame({ type: "relevo.mode", on: mode });
      postToFrame({ type: "relevo.pins", annotations: (doc && doc.annotations) || [] });
      return;
    }
    if (msg.type === "relevo.pick") {
      openDraft(msg.selector || "", msg.x, msg.y);
      return;
    }
    if (msg.type === "relevo.orphans") {
      list.setOrphans(msg.ids);
      return;
    }
    if (msg.type === "relevo.points") {
      list.setPoints(msg.points);
      return;
    }
    if (msg.type === "relevo.pin") {
      // A pin was clicked on the board. Open that thread's card at the pin, and
      // mark the matching list row so the two views stay in step.
      list.setPoints([]);
      list.openCard(msg.id, msg.x, msg.y);
    }
  }
  window.addEventListener("message", onFrameMessage);

  // openDraft takes a pick and opens the draft box. The selector is shown, not
  // editable: the overlay built it from the clicked element, and the shell does
  // not second-guess it.
  function openDraft(selector, x, y) {
    picked = { selector: selector, x: x, y: y };
    draft = "";
    draftEl.value = "";
    selEl.textContent = selector ? selector : "board";
    setDirty(true);
    draftEl.focus();
  }

  // setDirty marks a draft worth keeping, which is what makes the poll show the
  // banner instead of re-rendering the frame under the user.
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
      list.setDocument(doc);
    }).catch(function (err) {
      api.setStatus("could not post the comment: " + err.message);
    });
  }

  // startPolling asks for the board every two seconds with the etags already
  // held: a 204 means nothing moved, anything else is the whole document again.
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

  // onBoardDocument applies a fresh document or holds it back: a clean page
  // takes it straight away, a page with a draft open is told and keeps what it
  // was writing. Nothing is ever merged into a draft.
  function onBoardDocument(next) {
    if (!dirty) {
      adopt(next);
      return;
    }
    pending = next;
    showStale();
  }

  // adopt installs a document as the current one and draws it.
  //
  // The frame is re-rendered only when the board itself moved. A new annotations
  // etag redraws the pins and nothing else: re-rendering on every comment would
  // tear down the whole document -- losing scroll position, the page's own state
  // and the click in flight -- to show a change that is only pins.
  function adopt(next) {
    var boardMoved = !doc || next.isNew || (next.html || "") !== (doc.html || "");
    doc = next;
    aetag = next.annotationsEtag || "";
    if (boardMoved) api.render(next.html);
    postToFrame({ type: "relevo.pins", annotations: next.annotations || [] });
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

  // showStale names the change and offers the update without taking it. The
  // draft underneath is untouched.
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

  commentBtn.addEventListener("click", function () { setMode(!mode); });
  closeBtn.addEventListener("click", function () { setMode(false); });
  postBtn.addEventListener("click", post);
  applyBtn.addEventListener("click", apply);
  draftEl.addEventListener("input", function () { draft = draftEl.value; });

  // Escape unwinds one layer at a time: the card if open, then the panel.
  document.addEventListener("keydown", function (ev) {
    if (ev.key !== "Escape") return;
    if (list.cardOpen()) {
      list.closeCard();
      commentBtn.focus();
      return;
    }
    if (mode) setMode(false);
  });

  // A click on the canvas is a pick, not a dismissal: the frame owns that
  // gesture. A click on the chrome around the frame is a dismissal.
  document.addEventListener("click", function (ev) {
    if (!mode) return;
    var frame = document.getElementById("frame");
    if (frame && frame.contains(ev.target)) return;
    if (pane.contains(ev.target)) return;
    // The toggle opened the panel with this same click as it bubbles up; it
    // must not dismiss what it just opened.
    if (commentBtn.contains(ev.target)) return;
    if (list.cardOpen()) {
      list.closeCard();
      return;
    }
    setMode(false);
  });

  // boot takes the first board document shell.js publishes, so the files share
  // one read and one token rather than each fetching.
  function boot() {
    if (api.onDocument) api.onDocument(function (next) {
      doc = next;
      aetag = next.annotationsEtag || "";
      postToFrame({ type: "relevo.pins", annotations: next.annotations || [] });
      list.setDocument(doc);
      requestPoints();
      startPolling();
    });
  }
  boot();

  window.__relevoComments = { setMode: setMode, focus: list.focus };
})();
