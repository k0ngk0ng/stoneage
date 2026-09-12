"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

class ClassList {
  constructor() { this.values = new Set(); }
  add(name) { this.values.add(name); }
  remove(name) { this.values.delete(name); }
  contains(name) { return this.values.has(name); }
}

class Element {
  constructor(id) {
    this.id = id || "";
    this.dataset = {};
    this.hidden = false;
    this.textContent = "";
    this.listeners = new Map();
    this.classList = new ClassList();
    this.focusCount = 0;
  }
  querySelector(selector) {
    if (selector === "#audit-detail-content" && this.content) return this.content;
    if (selector === "button[data-audit-detail-cancel]" && this.closeButton) return this.closeButton;
    return null;
  }
  querySelectorAll(selector) {
    if (selector === "[data-audit-detail-cancel]") return [this.backdrop, this.closeButton].filter(Boolean);
    return [];
  }
  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(listener);
  }
  dispatchEvent(event) {
    const current = event || {};
    current.type = current.type || "event";
    current.target = current.target || this;
    current.preventDefault = current.preventDefault || function () { current.defaultPrevented = true; };
    for (const listener of this.listeners.get(current.type) || []) listener(current);
  }
  focus() {
    this.focusCount += 1;
    this.ownerDocument.activeElement = this;
  }
}

function makeDocument() {
  const document = {
    activeElement: null,
    documentElement: {lang: "zh-CN"},
    body: {classList: new ClassList()},
    listeners: new Map(),
    querySelectorAll(selector) {
      if (selector === "[data-local-time]") return [];
      if (selector === "[data-audit-detail]") return [button];
      return [];
    },
    getElementById(id) {
      if (id === "audit-detail-modal") return modal;
      return null;
    },
    addEventListener(type, listener) {
      if (!this.listeners.has(type)) this.listeners.set(type, []);
      this.listeners.get(type).push(listener);
    },
    dispatchEvent(event) {
      event.preventDefault = event.preventDefault || function () { event.defaultPrevented = true; };
      for (const listener of this.listeners.get(event.type) || []) listener(event);
    }
  };
  const modal = new Element("audit-detail-modal");
  modal.ownerDocument = document;
  modal.hidden = true;
  const content = new Element("audit-detail-content");
  const backdrop = new Element();
  const closeButton = new Element();
  const button = new Element();
  content.ownerDocument = document;
  backdrop.ownerDocument = document;
  closeButton.ownerDocument = document;
  button.ownerDocument = document;
  button.dataset.auditDetail = "<img src=x onerror=alert(1)>\n第二行";
  modal.content = content;
  modal.backdrop = backdrop;
  modal.closeButton = closeButton;
  document.activeElement = button;
  return {document, modal, content, backdrop, closeButton, button};
}

const template = fs.readFileSync(__dirname + "/templates/audit.html", "utf8");
const source = fs.readFileSync(__dirname + "/static/app.js", "utf8");
assert.match(template, /data-audit-detail="\{\{\.Detail\}\}"/);
assert.doesNotMatch(template, /<td>\{\{\.Detail\}\}<\/td>/);
assert.match(template, /id="audit-detail-modal"/);
assert.match(template, /id="audit-detail-content"/);
assert.match(source, /auditDetailContent\.textContent/);
assert.doesNotMatch(source, /innerHTML/);

const fixture = makeDocument();
vm.runInNewContext(source, {document: fixture.document, Intl, Date, Number});
fixture.button.dispatchEvent({type: "click"});
assert.equal(fixture.modal.hidden, false, "detail button opens the modal");
assert.equal(fixture.content.textContent, fixture.button.dataset.auditDetail, "detail is rendered as text");
assert(fixture.document.body.classList.contains("modal-open"));
assert.equal(fixture.document.activeElement, fixture.closeButton, "close button receives focus");
fixture.document.dispatchEvent({type: "keydown", key: "Escape"});
assert.equal(fixture.modal.hidden, true, "Escape closes the modal");
assert.equal(fixture.document.activeElement, fixture.button, "focus returns to the detail button");
assert.equal(fixture.document.body.classList.contains("modal-open"), false);
fixture.button.dispatchEvent({type: "click"});
fixture.backdrop.dispatchEvent({type: "click"});
assert.equal(fixture.modal.hidden, true, "backdrop closes the modal");

console.log("admin audit detail modal tests passed");
