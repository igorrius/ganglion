// The page polls api/data and draws what it gets. Everything from the data
// goes in with textContent and attributes of elements made here: nothing is
// parsed as markup, so a notice can never become part of the page.
(function () {
  "use strict";
  var POLL_MS = 15000;
  var SVG = "http://www.w3.org/2000/svg";

  function $(id) { return document.getElementById(id); }

  function el(name, attrs, text) {
    var e = document.createElementNS(SVG, name);
    Object.keys(attrs || {}).forEach(function (k) { e.setAttribute(k, String(attrs[k])); });
    if (text !== undefined) { e.textContent = text; }
    return e;
  }

  function fmt(n) {
    return typeof n === "number" && isFinite(n) ? String(Math.round(n * 100) / 100) : String(n);
  }

  function clock(t) {
    var d = new Date(t * 1000);
    return ("0" + d.getHours()).slice(-2) + ":" + ("0" + d.getMinutes()).slice(-2);
  }

  function age(s) {
    if (s < 90) { return s + " s ago"; }
    if (s < 5400) { return Math.round(s / 60) + " min ago"; }
    return Math.round(s / 3600) + " h ago";
  }

  function drawChart(points) {
    var box = $("chart");
    box.textContent = "";
    if (points.length < 2) { box.textContent = "Not enough readings yet."; return; }
    var w = 600, h = 220, left = 40, bottom = 20, top = 8, right = 8;
    var t0 = points[0][0], t1 = points[points.length - 1][0];
    var lo = Infinity, hi = -Infinity;
    points.forEach(function (p) { lo = Math.min(lo, p[1]); hi = Math.max(hi, p[1]); });
    if (lo === hi) { lo -= 1; hi += 1; }
    var x = function (t) { return left + (w - left - right) * ((t - t0) / Math.max(1, t1 - t0)); };
    var y = function (v) { return top + (h - top - bottom) * (1 - (v - lo) / (hi - lo)); };
    var svg = el("svg", { viewBox: "0 0 " + w + " " + h, preserveAspectRatio: "xMidYMid meet" });
    svg.appendChild(el("line", { "class": "axis", x1: left, y1: top, x2: left, y2: h - bottom }));
    svg.appendChild(el("line", { "class": "axis", x1: left, y1: h - bottom, x2: w - right, y2: h - bottom }));
    svg.appendChild(el("text", { x: 4, y: top + 10 }, fmt(hi)));
    svg.appendChild(el("text", { x: 4, y: h - bottom }, fmt(lo)));
    svg.appendChild(el("text", { x: left, y: h - 4 }, clock(t0)));
    svg.appendChild(el("text", { x: w - right - 28, y: h - 4 }, clock(t1)));
    var path = points.map(function (p) { return fmt(x(p[0])) + "," + fmt(y(p[1])); }).join(" ");
    svg.appendChild(el("polyline", { "class": "line", points: path }));
    box.appendChild(svg);
  }

  function drawNotices(list) {
    var ul = $("notices");
    ul.textContent = "";
    if (!list.length) {
      var none = document.createElement("li");
      none.textContent = "No notices.";
      ul.appendChild(none);
      return;
    }
    list.forEach(function (n) {
      var li = document.createElement("li");
      var when = document.createElement("time");
      when.textContent = n.at;
      var what = document.createElement("span");
      what.textContent = n.text;
      li.appendChild(when);
      li.appendChild(what);
      ul.appendChild(li);
    });
  }

  function draw(d) {
    $("current").textContent = d.current ? fmt(d.current.value) : "no data";
    $("age").textContent = d.current ? age(Math.max(0, d.now - d.current.at)) : "";
    var vs = d.points.map(function (p) { return p[1]; });
    $("range").textContent = vs.length ? fmt(Math.min.apply(null, vs)) + " – " + fmt(Math.max.apply(null, vs)) : "–";
    drawChart(d.points);
    drawNotices(d.notices);
    $("status").textContent = "";
  }

  function poll() {
    fetch("api/data", { headers: { accept: "application/json" } })
      .then(function (r) { if (!r.ok) { throw new Error("status " + r.status); } return r.json(); })
      .then(draw)
      .catch(function () { $("status").textContent = "No answer; trying again."; });
  }

  poll();
  setInterval(poll, POLL_MS);
})();
