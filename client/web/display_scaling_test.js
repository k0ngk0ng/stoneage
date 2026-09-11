const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const html = fs.readFileSync(__dirname + '/index.html', 'utf8');
const source = html.slice(html.indexOf('  const DISPLAY_MODE_KEY='), html.indexOf('  /* A focused title input', html.indexOf('  function fitLegacyViewport()')));
function harness(width, height, dpr, saved) {
  const properties = {}, storage = new Map(saved ? [['stoneage:web:display-mode', saved]] : []);
  const root = {style: {setProperty: (k, v) => properties[k] = v}};
  const context = {window: {devicePixelRatio: dpr, localStorage: {getItem: k => storage.get(k), setItem: (k,v) => storage.set(k,v)}},
    document: {documentElement: root, body: {}, querySelector: () => null}, viewportForScene: () => ({width,height}),
    loginInputViewportLock: false, rememberStableViewport() {}, scheduleWorldLabelRefresh() {}, app: {phase:'login'}, settleLegacyViewport() {vm.runInContext('fitLegacyViewport()', context);}};
  vm.createContext(context); vm.runInContext(source + '\nfitLegacyViewport()', context);
  return {context, properties, storage};
}
for (const [width,height,dpr,expected] of [[1920,1080,1,2],[2560,1440,1,3],[3840,2160,1,4],[1920,1080,2,2],[1536,864,1.25,1.6],[800,600,0.5,1.25],[390,844,3,390/640],[844,390,3,390/480]]) {
  const h=harness(width,height,dpr,"crisp");
  const scale=Number(h.properties['--scene-scale']);
  assert.equal(scale,expected,`${width}×${height} DPR ${dpr}`);
  assert(640*scale<=width && 480*scale<=height);
  if(width>=640 && height>=480 && expected*dpr>=1)assert(Math.abs(scale*dpr-Math.round(scale*dpr))<1e-8);
  for(const [axis,size,logical] of [['x',width,640],['y',height,480]]) {
    const origin=(size-logical*scale)/2+parseFloat(h.properties[`--scene-offset-${axis}`]);
    assert(Math.abs(origin*dpr-Math.round(origin*dpr))<1e-8,'scene origin must align to physical pixels');
  }
}
for (const [width,height] of [[1920,1080],[1536,864],[390,844],[844,390]]) {
  assert.equal(Number(harness(width,height,1).properties['--scene-scale']),Math.min(width/640,height/480),'default mode must fit the window');
}
const h=harness(1920,1080,1,'fit');
assert.equal(Number(h.properties['--scene-scale']),2.25);
vm.runInContext('setDisplayMode("crisp")',h.context);
assert.equal(Number(h.properties['--scene-scale']),2);
assert.equal(h.storage.get('stoneage:web:display-mode'),'crisp');
vm.runInContext('setDisplayMode("fit")',h.context);
assert.equal(Number(h.properties['--scene-scale']),2.25);
assert.equal(h.storage.get('stoneage:web:display-mode'),'fit');
assert.equal(Number(harness(1920,1080,1,'invalid').properties['--scene-scale']),2.25);
console.log('display scaling: desktop, Retina, fractional DPR, mobile, pixel alignment and saved mode passed');
