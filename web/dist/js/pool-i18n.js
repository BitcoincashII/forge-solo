/* pool-i18n.js - Pool Translation Loader
   Reads lang cookie (domain=.bch2.org), falls back to browser language,
   loads pool-{lang}.js from /lang/, applies PT translations to data-i18n elements. */
(function() {
  "use strict";

  var SUPPORTED = ["en","de","es","fr","ja","ko","pt","ru","zh"];
  var DEFAULT_LANG = "en";

  function getCookie(name) {
    var m = document.cookie.match(new RegExp("(?:^|;\\s*)" + name + "=([^;]*)"));
    return m ? decodeURIComponent(m[1]) : null;
  }

  function detectLang() {
    // 1. Check cookie
    var c = getCookie("lang");
    if (c && SUPPORTED.indexOf(c) !== -1) return c;

    // 2. Browser language
    var nav = (navigator.language || navigator.userLanguage || "").toLowerCase();
    // Try exact match first (e.g., "pt-br" -> "pt")
    var short = nav.split("-")[0];
    if (SUPPORTED.indexOf(short) !== -1) return short;

    return DEFAULT_LANG;
  }

  function applyTranslations() {
    if (typeof PT === "undefined") return;

    // Set html lang attribute
    if (PT._code) {
      document.documentElement.lang = PT._code;
    }

    // Apply translations to all elements with data-i18n
    var els = document.querySelectorAll("[data-i18n]");
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      var key = el.getAttribute("data-i18n");
      if (PT[key] !== undefined) {
        // Check if the element has an input child or is an input/textarea
        if (el.tagName === "INPUT" || el.tagName === "TEXTAREA") {
          el.placeholder = PT[key];
        } else {
          el.innerHTML = PT[key];
        }
      }
    }

    // Update language switcher link if present
    var langSwitch = document.getElementById("langSwitch");
    if (langSwitch && PT._code) {
      langSwitch.textContent = "\uD83C\uDF10 " + PT._code.toUpperCase();
    }
  }

  var lang = detectLang();

  // If non-English, load the translation file dynamically
  if (lang !== DEFAULT_LANG) {
    var script = document.createElement("script");
    script.src = "/lang/pool-" + lang + ".js?v=20260715";
    script.onload = function() {
      applyTranslations();
    };
    script.onerror = function() {
      // Fallback: keep English (already loaded)
      applyTranslations();
    };
    document.head.appendChild(script);
  } else {
    // English is already loaded via the static script tag
    applyTranslations();
  }
})();
