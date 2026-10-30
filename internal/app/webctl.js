// posse web 在页面里用的函数，挂在 window.__posse 上。每次 Eval 前都带上这段（已定义就跳过），
// 页面载入时也经 PreloadScript 注入一次，console 从页面自己的脚本运行之前就开始记。
(() => {
  if (window.__posse) return;
  const logs = [];
  const fmt = (v) => {
    if (typeof v === "string") return v;
    if (v instanceof Error) return v.stack || String(v);
    try {
      return JSON.stringify(v);
    } catch {
      return String(v);
    }
  };
  const record = (level, args) => {
    logs.push({ level, text: Array.from(args, fmt).join(" "), time: Date.now() });
    if (logs.length > 200) logs.shift();
  };
  for (const level of ["log", "info", "warn", "error", "debug"]) {
    const orig = console[level];
    console[level] = function (...args) {
      record(level, args);
      return orig.apply(this, args);
    };
  }
  addEventListener("error", (e) => {
    // 没有出处的 "Script error." 是 posse web eval 传语句时先按表达式编译失败的那一次，或跨域脚本的错误，没有内容
    if (!e.filename && e.message === "Script error.") return;
    record("error", [`${e.message} (${e.filename}:${e.lineno})`]);
  });
  addEventListener("unhandledrejection", (e) => record("error", ["Unhandled rejection:", e.reason]));

  let seq = 0;
  const implicitRole = (el) => {
    const tag = el.localName;
    switch (tag) {
      case "a":
        return el.hasAttribute("href") ? "link" : "";
      case "button":
      case "summary":
        return "button";
      case "select":
        return el.multiple || el.size > 1 ? "listbox" : "combobox";
      case "textarea":
        return "textbox";
      case "input": {
        const t = (el.type || "text").toLowerCase();
        if (t === "hidden") return "";
        if (["button", "submit", "reset", "image"].includes(t)) return "button";
        if (t === "checkbox") return "checkbox";
        if (t === "radio") return "radio";
        if (t === "range") return "slider";
        if (t === "search") return "searchbox";
        return "textbox";
      }
      case "h1":
      case "h2":
      case "h3":
      case "h4":
      case "h5":
      case "h6":
        return "heading";
      case "nav":
        return "navigation";
      case "main":
        return "main";
      case "form":
        return "form";
      case "dialog":
        return "dialog";
      case "img":
        return el.getAttribute("alt") ? "img" : "";
    }
    if (el.isContentEditable && !el.parentElement?.isContentEditable) return "textbox";
    return "";
  };
  const interactive = new Set([
    "link", "button", "checkbox", "radio", "combobox", "listbox", "textbox", "searchbox", "slider",
    "switch", "tab", "menuitem", "menuitemcheckbox", "menuitemradio", "option", "spinbutton", "treeitem",
  ]);
  const structural = new Set(["heading", "navigation", "main", "form", "dialog", "alertdialog", "alert", "img", "tablist", "menu"]);
  const visible = (el) => {
    if (el.closest("[aria-hidden=true]")) return false;
    const r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return false;
    const s = getComputedStyle(el);
    return s.visibility !== "hidden" && s.display !== "none" && s.opacity !== "0";
  };
  const clip = (s, n = 80) => {
    s = (s || "").replace(/\s+/g, " ").trim();
    return s.length > n ? s.slice(0, n - 1) + "…" : s;
  };
  // labelText 是 label 自己的文字：包在里面的输入框、下拉框（连同选项）不算
  const labelText = (l) => {
    const c = l.cloneNode(true);
    c.querySelectorAll("input,select,textarea,button").forEach((n) => n.remove());
    return c.textContent;
  };
  const nameOf = (el, role) => {
    const by = el.getAttribute("aria-labelledby");
    if (by) {
      const t = by.split(/\s+/).map((id) => document.getElementById(id)?.innerText || "").join(" ");
      if (t.trim()) return clip(t);
    }
    const label = el.getAttribute("aria-label");
    if (label) return clip(label);
    if (el.labels && el.labels.length) return clip(Array.from(el.labels, labelText).join(" "));
    // 地标、表单的文字是里面所有元素的，只认显式的名字
    if (structural.has(role) && !["heading", "img"].includes(role)) return "";
    if (el.localName === "input" && ["button", "submit", "reset"].includes(el.type)) return clip(el.value);
    for (const a of ["alt", "title", "placeholder"]) {
      const v = el.getAttribute(a);
      if (v) return clip(v);
    }
    if (["input", "select", "textarea"].includes(el.localName)) return "";
    return clip(el.innerText);
  };
  const describe = (el, role) => {
    let line = role;
    if (role === "heading") {
      const level = el.getAttribute("aria-level") || (/^h[1-6]$/.test(el.localName) ? el.localName[1] : "");
      if (level) line += ` level=${level}`;
    }
    const name = nameOf(el, role);
    if (name) line += ` ${JSON.stringify(name)}`;
    if (el.localName === "select") {
      const opt = el.selectedOptions[0];
      if (opt) line += ` value=${JSON.stringify(clip(opt.text))}`;
    } else if (["input", "textarea"].includes(el.localName) && !["checkbox", "radio", "button", "submit", "reset"].includes(el.type)) {
      if (el.value) line += ` value=${JSON.stringify(el.type === "password" ? "••••" : clip(el.value))}`;
    } else if (el.isContentEditable && role === "textbox") {
      line += ` value=${JSON.stringify(clip(el.innerText))}`;
    }
    if (el.checked || el.getAttribute("aria-checked") === "true") line += " checked";
    if (el.getAttribute("aria-selected") === "true") line += " selected";
    if (el.getAttribute("aria-expanded")) line += ` expanded=${el.getAttribute("aria-expanded")}`;
    if (el.disabled || el.getAttribute("aria-disabled") === "true") line += " disabled";
    if (el === document.activeElement) line += " focused";
    if (role === "link") {
      const href = el.getAttribute("href");
      if (href && !href.startsWith("javascript:")) line += ` -> ${clip(href, 120)}`;
    }
    return line;
  };
  const refOf = (el) => {
    let ref = el.getAttribute("data-posse-ref");
    if (!ref) {
      ref = "e" + ++seq;
      el.setAttribute("data-posse-ref", ref);
    }
    return ref;
  };

  // snapshot 输出可见的可交互元素和标题、地标，按 DOM 嵌套缩进；可交互元素带 ref。
  const snapshot = (max = 600) => {
    const lines = [];
    let truncated = false;
    const walk = (node, depth) => {
      for (const el of node.children) {
        if (lines.length >= max) {
          truncated = true;
          return;
        }
        const role = el.getAttribute("role") || implicitRole(el);
        let next = depth;
        if ((interactive.has(role) || structural.has(role)) && visible(el)) {
          const ref = interactive.has(role) ? `[${refOf(el)}] ` : "";
          lines.push("  ".repeat(depth) + "- " + ref + describe(el, role));
          next = depth + 1;
          // 链接、按钮里面的文字已经是它的名字，不再往下
          if (["link", "button", "option", "menuitem", "tab", "heading"].includes(role)) continue;
        }
        if (el.shadowRoot) walk(el.shadowRoot, next);
        walk(el, next);
      }
    };
    if (document.body) walk(document.body, 0);
    return { url: location.href, title: document.title, tree: lines.join("\n"), count: lines.length, truncated };
  };

  // deepQuery 也找打开的 shadow root 里面：snapshot 会给那里的元素编 ref
  const deepQuery = (root, sel) => {
    const hit = root.querySelector(sel);
    if (hit) return hit;
    for (const el of root.querySelectorAll("*")) {
      if (el.shadowRoot) {
        const inner = deepQuery(el.shadowRoot, sel);
        if (inner) return inner;
      }
    }
    return null;
  };
  const find = (target) => {
    const el = /^e\d+$/.test(target) ? deepQuery(document, `[data-posse-ref="${target}"]`) : document.querySelector(target);
    if (!el) throw new Error(`no element matches ${target}; run posse web snapshot for fresh refs`);
    return el;
  };

  const click = (target) => {
    const el = find(target);
    el.scrollIntoView({ block: "center", inline: "center" });
    const r = el.getBoundingClientRect();
    const at = { bubbles: true, cancelable: true, composed: true, view: window, clientX: r.left + r.width / 2, clientY: r.top + r.height / 2, button: 0 };
    el.dispatchEvent(new PointerEvent("pointerdown", { ...at, pointerType: "mouse", isPrimary: true }));
    el.dispatchEvent(new MouseEvent("mousedown", at));
    if (el.focus) el.focus();
    el.dispatchEvent(new PointerEvent("pointerup", { ...at, pointerType: "mouse", isPrimary: true }));
    el.dispatchEvent(new MouseEvent("mouseup", at));
    // 派发的 click 也会触发链接跳转、复选框切换、表单提交这些默认动作
    el.dispatchEvent(new MouseEvent("click", { ...at, detail: 1 }));
    return { url: location.href };
  };

  // fill 照用户输入改值：React 这类框架记着上一次的值，要经原型上的 setter 改，再发 input、change。
  const fill = (target, text) => {
    const el = find(target);
    el.scrollIntoView({ block: "center" });
    el.focus();
    if (el.localName === "select") {
      const opt = Array.from(el.options).find((o) => o.value === text || o.text.trim() === text);
      if (!opt) throw new Error(`no option ${JSON.stringify(text)} in ${target}`);
      el.value = opt.value;
    } else if (el.localName === "input" || el.localName === "textarea") {
      const proto = el.localName === "input" ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
      Object.getOwnPropertyDescriptor(proto, "value").set.call(el, text);
    } else if (el.isContentEditable) {
      el.textContent = text;
    } else {
      throw new Error(`${target} is not a text field, select or editable element`);
    }
    el.dispatchEvent(new Event("input", { bubbles: true, composed: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
    return { url: location.href };
  };

  const keyCodes = { Enter: 13, Tab: 9, Escape: 27, Backspace: 8, Delete: 46, Space: 32, ArrowUp: 38, ArrowDown: 40, ArrowLeft: 37, ArrowRight: 39, Home: 36, End: 35, PageUp: 33, PageDown: 34 };
  const focusables = () =>
    Array.from(document.querySelectorAll("a[href],button,input,select,textarea,[tabindex],[contenteditable]")).filter(
      (el) => !el.disabled && el.tabIndex >= 0 && visible(el),
    );

  // press 派发的是脚本合成的按键，浏览器不会照真按键做默认动作，常用的几个在这里补上。
  const press = (key, target) => {
    const el = target ? find(target) : document.activeElement || document.body;
    if (target) el.focus();
    const k = key === "Space" ? " " : key;
    const init = { key: k, code: key.length === 1 ? "Key" + key.toUpperCase() : key, keyCode: keyCodes[key] || k.toUpperCase().charCodeAt(0), bubbles: true, cancelable: true, composed: true };
    const go = el.dispatchEvent(new KeyboardEvent("keydown", init));
    if (go && k.length === 1) el.dispatchEvent(new KeyboardEvent("keypress", init));
    if (go) {
      if (key === "Enter") {
        if (el.localName === "input" && el.form) el.form.requestSubmit();
        else if (["a", "button", "summary"].includes(el.localName) || el.getAttribute("role") === "button") el.click();
      } else if (key === "Tab") {
        const list = focusables();
        const next = list[(list.indexOf(el) + 1) % list.length];
        if (next) next.focus();
      } else if (key === "Space" && (el.type === "checkbox" || el.type === "radio" || el.localName === "button")) {
        el.click();
      }
    }
    el.dispatchEvent(new KeyboardEvent("keyup", init));
    return { url: location.href };
  };

  const check = (kind, arg) => {
    switch (kind) {
      case "load":
        return document.readyState === "complete";
      case "selector": {
        const el = document.querySelector(arg);
        return !!el && visible(el);
      }
      case "text":
        return !!document.body && document.body.innerText.includes(arg);
    }
    throw new Error(`unknown wait kind ${kind}`);
  };

  Object.defineProperty(window, "__posse", {
    value: Object.freeze({ snapshot, click, fill, press, check, console: () => logs.slice() }),
  });
})();
