"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const {spawnSync} = require("node:child_process");

if (!process.argv.includes("--child")) {
  for (const zone of ["Asia/Shanghai", "America/New_York"]) {
    const result = spawnSync(process.execPath, [__filename, "--child"], {
      env: {...process.env, TZ: zone}, encoding: "utf8"
    });
    assert.equal(result.status, 0, result.stdout + result.stderr);
  }
  console.log("admin local-time tests passed (Shanghai and New York, including DST)");
} else {
  const elements = [
    {dataset: {localTime: "2024-01-02T03:04:05Z"}, textContent: "UTC"},
    {dataset: {localTime: "2024-07-02T03:04:05Z"}, textContent: "UTC"},
    {dataset: {localTime: "invalid"}, textContent: "unchanged"}
  ];
  const source = fs.readFileSync(__dirname + "/static/app.js", "utf8");
  vm.runInNewContext(source, {Intl, document: {
    documentElement: {lang:"zh-CN"}, querySelectorAll: () => elements, getElementById: () => null
  }});
  const expected = process.env.TZ === "Asia/Shanghai"
    ? [["2024-01-02", "11:04:05"], ["2024-07-02", "11:04:05"]]
    : [["2024-01-01", "22:04:05"], ["2024-07-01", "23:04:05"]];
  // Use a stable locale with the real local timezone to verify the displayed
  // calendar date/time independently of the machine's language preference.
  for (let i = 0; i < 2; i++) {
    const original = new Date(elements[i].dataset.localTime);
    const date = new Intl.DateTimeFormat("sv-SE", {year:"numeric",month:"2-digit",day:"2-digit"}).format(original);
    const time = new Intl.DateTimeFormat("en-GB", {hour:"2-digit",minute:"2-digit",second:"2-digit",hourCycle:"h23"}).format(original);
    assert.deepEqual([date, time], expected[i]);
    assert.equal(elements[i].textContent, new Intl.DateTimeFormat("zh-CN", {
      year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit",second:"2-digit",hourCycle:"h23"
    }).format(original));
  }
  assert.equal(elements[2].textContent, "unchanged");
  for (const name of ["accounts", "account_detail", "audit", "releases", "assets", "server"]) {
    assert.match(fs.readFileSync(__dirname + "/templates/" + name + ".html", "utf8"), /data-local-time=/);
  }
}
