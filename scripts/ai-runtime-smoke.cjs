#!/usr/bin/env node
// Offline image gate: real runner and Codex, synthetic Responses provider.
// No operator config, real credentials, external provider or game is used.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const {spawn} = require('node:child_process');

async function main() {
  assert.notEqual(process.getuid(), 0, 'run the image gate as its non-root user');
  const parent = process.argv[2];
  assert(parent && path.isAbsolute(parent), 'explicit absolute temporary-state parent required');
  const root = fs.mkdtempSync(path.join(parent, 'image-smoke-'));
  fs.chmodSync(root, 0o700);
  const expected = 'STONEAGE_IMAGE_RESPONSES_OK';
  const key = 'image-smoke-dummy-provider-key';
  const token = Buffer.alloc(32, 7).toString('base64url');
  const requests = [];
  const unexpected = [];
  const server = http.createServer(async (req, res) => {
    try {
      let raw = '';
      for await (const chunk of req) {
        raw += chunk;
        assert(raw.length <= 8 * 1024 * 1024);
      }
      assert.equal(req.method, 'POST');
      assert.equal(req.url, '/responses');
      assert.equal(req.headers.authorization, `Bearer ${key}`);
      const body = JSON.parse(raw);
      assert.equal(body.model, 'stoneage-image-smoke');
      assert.equal(body.stream, true);
      requests.push(body);
      const id = `resp-smoke-${requests.length}`;
      const itemID = `msg-smoke-${requests.length}`;
      const part = {type: 'output_text', text: expected, annotations: []};
      const item = {id: itemID, type: 'message', role: 'assistant', status: 'completed', content: [part]};
      const response = (status, output) => ({id, object: 'response', status, model: body.model, output});
      res.writeHead(200, {'Content-Type': 'text/event-stream'});
      const send = (type, fields) => res.write(`event: ${type}\ndata: ${JSON.stringify({type, ...fields})}\n\n`);
      send('response.created', {response: response('in_progress', [])});
      send('response.output_item.added', {output_index: 0, item: {...item, status: 'in_progress', content: []}});
      send('response.content_part.added', {item_id: itemID, output_index: 0, content_index: 0, part: {...part, text: ''}});
      send('response.output_text.delta', {item_id: itemID, output_index: 0, content_index: 0, delta: expected});
      send('response.output_text.done', {item_id: itemID, output_index: 0, content_index: 0, text: expected});
      send('response.content_part.done', {item_id: itemID, output_index: 0, content_index: 0, part});
      send('response.output_item.done', {output_index: 0, item});
      send('response.completed', {response: response('completed', [item])});
      res.end('data: [DONE]\n\n');
    } catch (err) {
      unexpected.push(err.message);
      res.writeHead(400).end();
    }
  });
  try {
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', resolve);
    });
    const endpoint = `http://127.0.0.1:${server.address().port}`;
    const profile = 'image-smoke';
    const env = process.env;
    const runner = env.STONEAGE_AI_RUNNER_BINARY || '/usr/local/bin/stoneage-ai-runner';
    if (!env.STONEAGE_AI_RUNNER_BINARY) {
      // Local harnesses explicitly select their runner. The image build uses
      // the fixed production paths and must detect ENV contract drift.
      for (const [name, value] of Object.entries({
        STONEAGE_AI_CODEX_BINARY: '/usr/local/bin/codex',
        STONEAGE_AI_MCP_BINARY: '/usr/local/bin/stoneage-game-mcp',
        STONEAGE_AI_SKILL_ROOT: '/opt/stoneage/ai/skills',
        STONEAGE_AI_GIT_BINARY: '/usr/bin/git',
        STONEAGE_AI_STATE_ROOT: '/var/lib/stoneage-ai',
        HOME: '/var/lib/stoneage-ai',
      })) assert.equal(env[name], value, `image environment drift: ${name}`);
    }
    // Exercise the runner's actual environment defaults, just as the broker
    // does. Override only state so build-time threads never seed player data.
    const args = ['-profile', profile, '-state', root];
    async function turn(id, thread) {
      const request = {profile_id: profile, request_id: id,
        run_request: {prompt: `Image smoke ${id}: reply with the supplied response.`, ...(thread ? {resume: true, thread_id: thread} : {})},
        model: {provider: 'custom', base_url: endpoint, model: 'stoneage-image-smoke', api_key: key},
        skills: [{name: 'stoneage-play'}],
        mcp: {endpoint: `${endpoint}/v1/game`, token, character_id: 'image-character', generation: 1}};
      const output = await new Promise((resolve, reject) => {
        // A separate process group bounds a stuck CLI and its MCP descendants.
        const child = spawn(runner, args, {detached: true, stdio: ['pipe', 'pipe', 'pipe']});
        let stdout = '', stderr = '';
        const kill = () => { try { process.kill(-child.pid, 'SIGKILL'); } catch {} };
        const timeout = setTimeout(kill, 60000);
        child.stdout.on('data', chunk => { stdout += chunk; if (stdout.length > 8 * 1024 * 1024) kill(); });
        child.stderr.on('data', chunk => { stderr += chunk; if (stderr.length > 256 * 1024) kill(); });
        child.stdin.on('error', () => {});
        child.once('error', err => { clearTimeout(timeout); reject(err); });
        child.once('close', code => {
          clearTimeout(timeout);
          if (code !== 0) return reject(new Error(`runner failed: exit=${code}; ${stderr.slice(0, 512)}`));
          resolve({stdout, stderr});
        });
        child.stdin.end(JSON.stringify(request));
      });
      for (const secret of [key, token]) assert(!JSON.stringify(output).includes(secret), 'credential leaked to process output');
      const response = JSON.parse(output.stdout);
      assert.equal(response.ok, true);
      assert.equal(response.profile_id, profile);
      assert.equal(response.request_id, id);
      assert.equal(response.result.turn.status, 'completed');
      assert.equal(response.result.process.exit_code, 0);
      assert.equal(response.result.last_message, expected);
      assert(response.result.thread_id);
      return response.result.thread_id;
    }
    const thread = await turn('first');
    assert.equal(await turn('resume', thread), thread);
    assert.equal(requests.length, 2);
    assert.deepEqual(unexpected, []);
    assert(JSON.stringify(requests[1].input).includes('Image smoke first:'), 'resume lost the original prompt');
    const config = fs.readFileSync(path.join(root, 'codex', 'config.toml'), 'utf8');
    for (const [name, value] of [['approval_policy', 'never'], ['sandbox_mode', 'danger-full-access'], ['wire_api', 'responses']]) {
      assert(new RegExp(`${name}\\s*=\\s*['"]${value}['"]`).test(config), `missing fixed ${name}`);
    }
    assert(fs.readFileSync(path.join(root, 'workspaces', profile, '.agents', 'skills', 'stoneage-play', 'SKILL.md'), 'utf8').includes('game_observe'));
    console.log('AI image smoke passed: real runner/Codex, Responses, exact-thread resume, installed Skill and unattended config; synthetic provider, no game execution.');
  } finally {
    await new Promise(resolve => server.close(resolve));
    fs.rmSync(root, {recursive: true, force: true});
  }
}
main().catch(err => { console.error(err.message); process.exitCode = 1; });
