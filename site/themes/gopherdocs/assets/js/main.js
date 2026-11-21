// gopherdocs: theme toggle, mobile menu, copy buttons and the "on this page"
// highlight. The initial theme is set inline in <head>, before first paint.
(function () {
  var root = document.documentElement;

  var themeBtn = document.querySelector(".theme-btn");
  if (themeBtn) {
    themeBtn.addEventListener("click", function () {
      var next = root.getAttribute("data-theme") === "dark" ? "light" : "dark";
      root.setAttribute("data-theme", next);
      try { localStorage.setItem("theme", next); } catch (e) {}
    });
  }

  var menuBtn = document.querySelector(".menu-btn");
  if (menuBtn) {
    menuBtn.addEventListener("click", function () {
      var open = document.body.classList.toggle("nav-open");
      menuBtn.setAttribute("aria-expanded", open ? "true" : "false");
    });
    document.addEventListener("click", function (e) {
      if (!document.body.classList.contains("nav-open")) return;
      if (e.target.closest(".sidebar") || e.target.closest(".menu-btn")) return;
      document.body.classList.remove("nav-open");
      menuBtn.setAttribute("aria-expanded", "false");
    });
  }

  document.querySelectorAll(".highlight").forEach(function (block) {
    var code = block.querySelector("code");
    if (!code || !navigator.clipboard) return;
    var btn = document.createElement("button");
    btn.className = "copy-btn";
    btn.type = "button";
    btn.textContent = "Copy";
    btn.addEventListener("click", function () {
      navigator.clipboard.writeText(code.innerText.replace(/\n$/, "")).then(function () {
        btn.textContent = "Copied";
        setTimeout(function () { btn.textContent = "Copy"; }, 1500);
      });
    });
    block.appendChild(btn);
  });

  var tocLinks = document.querySelectorAll(".toc a[href^='#']");
  if (tocLinks.length && "IntersectionObserver" in window) {
    var byId = {};
    tocLinks.forEach(function (a) { byId[decodeURIComponent(a.getAttribute("href").slice(1))] = a; });
    var headings = Array.prototype.filter.call(
      document.querySelectorAll(".content h2[id], .content h3[id]"),
      function (h) { return byId[h.id]; });
    var current = null;
    var observer = new IntersectionObserver(function () {
      var top = null;
      headings.forEach(function (h) {
        if (h.getBoundingClientRect().top < 120) top = h;
      });
      if (!top) top = headings[0];
      if (top === current) return;
      if (current) byId[current.id].classList.remove("active");
      current = top;
      if (current) byId[current.id].classList.add("active");
    }, { rootMargin: "0px 0px -70% 0px", threshold: [0, 1] });
    headings.forEach(function (h) { observer.observe(h); });
  }
})();
