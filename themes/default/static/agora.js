// Live updates for the conversation page. The server sends messages and the
// participants panel as HTML rendered by the theme's own templates, so this
// script only places them; a theme can replace it entirely.
(function () {
  "use strict";
  // On a narrow screen, participants and details start collapsed behind a
  // toggle (style.css shows the toggle only there).
  var aside = document.querySelector(".details");
  var toggle = aside && aside.querySelector(".details-toggle");
  if (toggle) {
    var narrow = window.matchMedia("(max-width: 62rem)");
    var setCollapsed = function (on) {
      aside.classList.toggle("collapsed", on);
      toggle.setAttribute("aria-expanded", on ? "false" : "true");
    };
    toggle.hidden = false;
    setCollapsed(narrow.matches);
    narrow.addEventListener("change", function (ev) { setCollapsed(ev.matches); });
    toggle.addEventListener("click", function () { setCollapsed(!aside.classList.contains("collapsed")); });
  }

  var list = document.getElementById("messages");
  // A page of earlier messages has no data-events: it does not update.
  if (!list || !list.getAttribute("data-events") || !window.EventSource) return;
  var panel = document.getElementById("participants");
  var after = list.getAttribute("data-last-seq") || "0";
  var source = new EventSource(list.getAttribute("data-events") + "?after=" + encodeURIComponent(after));

  function nearBottom() {
    return window.innerHeight + window.scrollY >= document.body.scrollHeight - 120;
  }

  // Messages that arrive while the reader is scrolled up are counted on a
  // button above the composer; it clears at the bottom or when clicked.
  var pill = document.querySelector(".new-below");
  var pillCount = pill && pill.querySelector(".new-count");
  var unseen = 0;
  function seen() {
    unseen = 0;
    if (pill) pill.hidden = true;
  }
  if (pill) {
    pill.addEventListener("click", function () {
      if (list.lastElementChild) list.lastElementChild.scrollIntoView({ block: "end" });
      seen();
    });
    window.addEventListener("scroll", function () {
      if (unseen && nearBottom()) seen();
    }, { passive: true });
  }

  source.addEventListener("message", function (ev) {
    var stick = nearBottom();
    var tmp = document.createElement("template");
    tmp.innerHTML = ev.data.trim();
    var node = tmp.content.firstElementChild;
    if (!node || document.getElementById(node.id)) return;
    node.classList.add("new");
    list.appendChild(node);
    // Keep the seq gutter wide enough for the newest number.
    var width = ("#" + node.getAttribute("data-seq")).length;
    if (width > parseInt(list.style.getPropertyValue("--seq-width") || "0", 10)) {
      list.style.setProperty("--seq-width", width + "ch");
    }
    if (stick) {
      node.scrollIntoView({ block: "end" });
    } else if (pill) {
      unseen++;
      pillCount.textContent = unseen;
      pill.hidden = false;
    }
  });

  source.addEventListener("participants", function (ev) {
    if (panel) panel.innerHTML = ev.data;
  });

  var about = document.getElementById("about");
  source.addEventListener("about", function (ev) {
    if (about) about.innerHTML = ev.data;
  });

  source.addEventListener("status", function (ev) {
    var badge = document.getElementById("status");
    if (badge) {
      badge.textContent = ev.data;
      badge.className = "pill " + ev.data;
    }
    if (ev.data !== "open") {
      var form = document.querySelector("form.observe");
      if (form) form.remove();
      var closed = document.querySelector(".observe-closed");
      if (closed) closed.hidden = false;
    }
  });

  // Composer: grows with its text, sends on Ctrl/Cmd+Enter, and posts in the
  // background so the page keeps its place; the comment then arrives on the
  // event stream like any other message.
  var form = document.querySelector("form.observe");
  if (!form || !window.fetch) return;
  var box = form.querySelector("textarea");
  var send = form.querySelector("button");
  var error = form.querySelector(".observe-error");
  function grow() {
    box.style.height = "auto";
    box.style.height = box.scrollHeight + 2 + "px";
  }
  box.addEventListener("input", grow);
  grow();
  box.addEventListener("keydown", function (ev) {
    if (ev.key === "Enter" && (ev.ctrlKey || ev.metaKey)) {
      ev.preventDefault();
      form.requestSubmit();
    }
  });
  form.addEventListener("submit", function (ev) {
    ev.preventDefault();
    send.disabled = true;
    error.hidden = true;
    fetch(form.action, {
      method: "POST",
      body: new URLSearchParams(new FormData(form)),
      redirect: "manual",
      credentials: "same-origin",
    }).then(function (res) {
      if (res.type === "opaqueredirect" || res.ok) {
        box.value = "";
        grow();
        box.focus();
        return;
      }
      return res.text().then(function (html) {
        var doc = new DOMParser().parseFromString(html, "text/html");
        var notice = doc.querySelector(".notice");
        error.textContent = notice ? notice.textContent : "Could not send the comment.";
        error.hidden = false;
      });
    }).catch(function () {
      error.textContent = "Could not reach the server; your comment is still here.";
      error.hidden = false;
    }).finally(function () {
      send.disabled = false;
    });
  });
})();

// The conversation list reloads itself every data-refresh seconds while the
// tab is visible, so new conversations and activity show up. A page of
// older conversations (?before=) stays as it is.
(function () {
  "use strict";
  var box = document.getElementById("conversations");
  if (!box || !window.fetch || !window.DOMParser) return;
  var every = Number(box.getAttribute("data-refresh"));
  if (!(every > 0) || new URLSearchParams(location.search).has("before")) return;
  var busy = false;
  var refresh = function () {
    if (busy || document.hidden) return;
    busy = true;
    fetch(location.href, { credentials: "same-origin" }).then(function (res) {
      if (!res.ok) return;
      return res.text().then(function (html) {
        var fresh = new DOMParser().parseFromString(html, "text/html").getElementById("conversations");
        if (fresh && fresh.innerHTML !== box.innerHTML) box.innerHTML = fresh.innerHTML;
      });
    }).catch(function () {}).finally(function () { busy = false; });
  };
  setInterval(refresh, every * 1000);
  document.addEventListener("visibilitychange", function () { if (!document.hidden) refresh(); });
})();
