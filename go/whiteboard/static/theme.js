// theme — the per-device theme and hue. Applied to <html> BEFORE first paint (a
// classic, parser-blocking script, so there is no flash), saved in
// localStorage when the menu changes them, and handed to Datastar's $theme and
// $hue signals on load. (data-persist is a Datastar Pro attribute and does not
// exist in the open bundle, so this owns the persistence.)
(() => {
  const KEY = "whiteboard.theme";
  const read = () => {
    try { return Object.assign({ theme: "system", hue: 255 }, JSON.parse(localStorage.getItem(KEY) || "{}")); }
    catch (_) { return { theme: "system", hue: 255 }; }
  };
  const apply = (theme, hue) => {
    const html = document.documentElement;
    if (theme === "light" || theme === "dark") html.setAttribute("data-ui-theme", theme);
    else html.removeAttribute("data-ui-theme");
    html.style.setProperty("--hue", hue);
  };
  window.theme = {
    saved: read,
    set(theme, hue) {
      apply(theme, hue);
      try { localStorage.setItem(KEY, JSON.stringify({ theme, hue: Number(hue) })); } catch (_) {}
    },
  };
  const t = read();
  apply(t.theme, t.hue);
})();
