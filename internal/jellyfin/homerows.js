// Ether: draws Ether's Home rows (collections, trending, ...) on the
// Jellyfin web client's home screen, below the standard sections.
(function () {
  "use strict";
  var MARK = "etherRows";
  var cache = { at: 0, rows: null };
  var busy = false;

  function api() {
    return window.ApiClient && window.ApiClient.accessToken && window.ApiClient.accessToken() ? window.ApiClient : null;
  }

  function loadRows(client) {
    if (cache.rows && Date.now() - cache.at < 60000) return Promise.resolve(cache.rows);
    return fetch(client.getUrl("Ether/HomeRows"), {
      headers: { "X-Emby-Token": client.accessToken() },
    })
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (rows) {
        cache = { at: Date.now(), rows: rows };
        return rows;
      })
      .catch(function () { return []; });
  }

  function el(tag, props, children) {
    var e = document.createElement(tag);
    Object.keys(props || {}).forEach(function (k) {
      if (k === "style") e.style.cssText = props[k];
      else e.setAttribute(k, props[k]);
    });
    (children || []).forEach(function (c) { e.appendChild(typeof c === "string" ? document.createTextNode(c) : c); });
    return e;
  }

  function card(client, item) {
    var tag = item.ImageTags && item.ImageTags.Primary;
    var img = tag
      ? el("img", {
          src: client.getUrl("Items/" + item.Id + "/Images/Primary", { maxWidth: 320, quality: 90, tag: tag }),
          loading: "lazy",
          alt: "",
          style: "width:100%;aspect-ratio:2/3;object-fit:cover;border-radius:.3em;background:rgba(128,128,128,.25);display:block",
        })
      : el("div", { style: "width:100%;aspect-ratio:2/3;border-radius:.3em;background:rgba(128,128,128,.25)" });
    var sub = item.ProductionYear ? String(item.ProductionYear) : "";
    return el(
      "a",
      {
        href: "#/details?id=" + item.Id + "&serverId=" + item.ServerId,
        style: "flex:0 0 auto;width:9.5em;color:inherit;text-decoration:none",
        title: item.Name,
      },
      [
        img,
        el("div", { style: "margin-top:.35em;font-size:.9em;white-space:nowrap;overflow:hidden;text-overflow:ellipsis" }, [item.Name]),
        el("div", { style: "font-size:.8em;opacity:.65;min-height:1.2em" }, [sub]),
      ]
    );
  }

  function section(client, row) {
    return el("div", { class: "verticalSection", style: "margin-bottom:1.5em" }, [
      el("h2", { class: "sectionTitle sectionTitle-cards padded-left", style: "margin:.6em 0 .5em" }, [row.Name]),
      el(
        "div",
        { style: "display:flex;gap:.8em;overflow-x:auto;padding:0 3.3% .5em;scrollbar-width:thin" },
        row.Items.map(function (i) { return card(client, i); })
      ),
    ]);
  }

  function render() {
    var client = api();
    var host = document.querySelector("#indexPage .homeSectionsContainer");
    if (!client || !host || busy || host.querySelector("." + MARK)) return;
    busy = true;
    loadRows(client).then(function (rows) {
      busy = false;
      host = document.querySelector("#indexPage .homeSectionsContainer");
      if (!host || host.querySelector("." + MARK)) return;
      var box = el("div", { class: MARK });
      rows.forEach(function (r) { box.appendChild(section(client, r)); });
      host.appendChild(box);
    });
  }

  var timer = 0;
  function soon() {
    clearTimeout(timer);
    timer = setTimeout(render, 150);
  }
  new MutationObserver(soon).observe(document.documentElement, { childList: true, subtree: true });
  window.addEventListener("hashchange", soon);
  soon();
})();
