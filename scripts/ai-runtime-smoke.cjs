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
      const probeExpected = 'STONEAGE_CONNECTION_TEST_OK';
      const responseText = JSON.stringify(body).includes(probeExpected) ? probeExpected : expected;
      const part = {type: 'output_text', text: responseText, annotations: []};
      const item = {id: itemID, type: 'message', role: 'assistant', status: 'completed', content: [part]};
      const response = (status, output) => ({id, object: 'response', status, model: body.model, output});
      res.writeHead(200, {'Content-Type': 'text/event-stream'});
      const send = (type, fields) => res.write(`event: ${type}\ndata: ${JSON.stringify({type, ...fields})}\n\n`);
      send('response.created', {response: response('in_progress', [])});
      send('response.output_item.added', {output_index: 0, item: {...item, status: 'in_progress', content: []}});
      send('response.content_part.added', {item_id: itemID, output_index: 0, content_index: 0, part: {...part, text: ''}});
      send('response.output_text.delta', {item_id: itemID, output_index: 0, content_index: 0, delta: responseText});
      send('response.output_text.done', {item_id: itemID, output_index: 0, content_index: 0, text: responseText});
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
    // An old container can leave a lock containing a PID reused by its
    // replacement. Exercise the packaged worker, not just the runner.
    const workerState = fs.mkdtempSync(path.join(root, 'worker-state-'));
    fs.mkdirSync(path.join(workerState, 'worker'));
    fs.writeFileSync(path.join(workerState, 'worker', 'worker.lock'), `${process.pid}\n`, {mode: 0o600});
    let workerConnected = false;
    const workerServer = http.createServer((req, res) => {
      workerConnected = req.url === '/api/ai/worker/connect';
      req.resume();
      res.writeHead(401, {'Content-Type': 'application/json'}).end('{}');
    });
    await new Promise(resolve => workerServer.listen(0, '127.0.0.1', resolve));
    try {
      const output = await new Promise((resolve, reject) => {
        const child = spawn(env.STONEAGE_AI_WORKER_BINARY || '/usr/local/bin/stoneage-ai-worker', [
          '--state-root', workerState, '--profile', 'image-worker-smoke',
          '--endpoint', `http://127.0.0.1:${workerServer.address().port}/api/ai/worker`,
        ], {stdio: ['ignore', 'pipe', 'pipe']});
        let logs = '';
        const timeout = setTimeout(() => child.kill('SIGKILL'), 15000);
        child.stdout.on('data', chunk => { logs += chunk; });
        child.stderr.on('data', chunk => { logs += chunk; });
        child.once('error', err => { clearTimeout(timeout); reject(err); });
        child.once('close', code => { clearTimeout(timeout); resolve({code, logs}); });
      });
      assert(workerConnected, `worker did not reach connect with stale PID lock: ${output.logs}`);
      assert.equal(output.code, 1);
      assert.match(output.logs, /unauthorized/);
    } finally {
      await new Promise(resolve => workerServer.close(resolve));
    }
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
    async function turn(id, thread, options = {}) {
      const request = {profile_id: profile, request_id: id,
        ...(options.reviewedRequest ? {reviewed_request_id: options.reviewedRequest} : {}),
        ...(options.turnDeadline ? {turn_deadline_unix_ms: options.turnDeadline} : {}),
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
          if (code !== 0 && !options.expectedError) return reject(new Error(`runner failed: exit=${code}; ${stderr.slice(0, 512)}`));
          resolve({stdout, stderr, code});
        });
        child.stdin.end(JSON.stringify(request));
      });
      for (const secret of [key, token]) assert(!JSON.stringify(output).includes(secret), 'credential leaked to process output');
      const response = JSON.parse(output.stdout);
      if (options.expectedError) {
        assert.notEqual(output.code, 0);
        assert.equal(response.ok, false);
        assert.equal(response.error, options.expectedError);
        return;
      }
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

    // Reproduce an interrupted legacy checkpoint in the persistent profile
    // volume. A plain fresh turn must remain fenced. After the broker has
    // verified an operator review (covered by broker tests), its reviewed
    // request namespace must permit a fresh conversation and exact resume
    // without deleting or falsely completing the old unknown checkpoint.
    const legacyCheckpointPath = path.join(root, 'state', profile, 'thread.json');
    const interrupted = JSON.parse(fs.readFileSync(legacyCheckpointPath, 'utf8'));
    Object.assign(interrupted, {state: 'unknown', turn_status: 'unknown', turn_completed: false});
    fs.writeFileSync(legacyCheckpointPath, JSON.stringify(interrupted), {mode: 0o600});
    const preservedCheckpoint = fs.readFileSync(legacyCheckpointPath, 'utf8');
    await turn('unreviewed-fresh', null, {expectedError: 'checkpoint_recovery_required'});
    assert.equal(requests.length, 2, 'unreviewed checkpoint fence called the model');
    const reviewedOptions = {reviewedRequest: 'resume'};
    await turn('expired-reviewed', null, {...reviewedOptions, turnDeadline: Date.now() - 1000, expectedError: 'deadline_exceeded'});
    assert.equal(requests.length, 2, 'expired container request called the model');
    const replacementThread = await turn('reviewed-fresh', null, reviewedOptions);
    assert.notEqual(replacementThread, thread, 'review reused the interrupted thread');
    assert.equal(await turn('reviewed-resume', replacementThread, reviewedOptions), replacementThread);
    assert.equal(fs.readFileSync(legacyCheckpointPath, 'utf8'), preservedCheckpoint, 'review altered the old checkpoint');
    assert.equal(requests.length, 4);

    // Exercise the model-only connection path through the real packaged
    // runner. The request must not materialize a game token, owner marker,
    // Skill, or MCP server configuration, while still completing a real
    // Responses request through Codex.
    const probeState = fs.mkdtempSync(path.join(root, 'probe-state-'));
    const probeRequest = {profile_id: profile, request_id: 'pure-probe', probe: true,
      run_request: {prompt: 'Reply with exactly STONEAGE_CONNECTION_TEST_OK. Do not use tools.'},
      model: {provider: 'custom', base_url: endpoint, model: 'stoneage-image-smoke', api_key: key}};
    const probeOutput = await new Promise((resolve, reject) => {
      const child = spawn(runner, ['-profile', profile, '-state', probeState], {detached: true, stdio: ['pipe', 'pipe', 'pipe']});
      let stdout = '', stderr = '';
      const kill = () => { try { process.kill(-child.pid, 'SIGKILL'); } catch {} };
      const timeout = setTimeout(kill, 60000);
      child.stdout.on('data', chunk => { stdout += chunk; if (stdout.length > 8 * 1024 * 1024) kill(); });
      child.stderr.on('data', chunk => { stderr += chunk; if (stderr.length > 256 * 1024) kill(); });
      child.stdin.on('error', () => {});
      child.once('error', err => { clearTimeout(timeout); reject(err); });
      child.once('close', code => {
        clearTimeout(timeout);
        if (code !== 0) return reject(new Error(`pure probe runner failed: exit=${code}; ${stderr.slice(0, 512)}`));
        resolve({stdout, stderr});
      });
      child.stdin.end(JSON.stringify(probeRequest));
    });
    for (const secret of [key, token]) assert(!JSON.stringify(probeOutput).includes(secret), 'pure probe leaked credential to process output');
    const probeResponse = JSON.parse(probeOutput.stdout);
    assert.equal(probeResponse.ok, true);
    assert.equal(probeResponse.result.last_message, 'STONEAGE_CONNECTION_TEST_OK');
    const probeConfig = fs.readFileSync(path.join(probeState, 'codex', 'config.toml'), 'utf8');
    assert(!probeConfig.includes('mcp_servers'), 'pure probe unexpectedly configured MCP');
    assert(!fs.existsSync(path.join(probeState, '.stoneage-ai-owner.json')), 'pure probe created game owner marker');
    assert(!fs.existsSync(path.join(probeState, 'state', profile, 'game-capability.token')), 'pure probe created game token');
    assert(!fs.existsSync(path.join(probeState, 'workspaces', profile, '.agents', 'skills')), 'pure probe installed a Skill');
    assert.equal(requests.length, 5);
    console.log('AI image smoke passed: real runner/Codex, Responses, exact-thread resume, installed Skill, unattended config, expired-request rejection, reviewed-checkpoint recovery and pure model Probe without MCP/game state; synthetic provider, no game execution.');
  } finally {
    await new Promise(resolve => server.close(resolve));
    fs.rmSync(root, {recursive: true, force: true});
  }
}
main().catch(err => { console.error(err.message); process.exitCode = 1; });
