import asyncio
import json
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from arena_agent.strategies import Basic, LLM, ProcessPlugin
from test_core import fixture


class ProviderTests(unittest.IsolatedAsyncioTestCase):
    async def test_chat_completions_over_http_and_deadline(self):
        team = fixture(3)
        expected = (await Basic().decide(team,[],time.monotonic()+1)).plan
        captured = []
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args): pass
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                captured.append(body)
                if self.path == '/slow': time.sleep(.4)
                data = json.dumps({'choices':[{'finish_reason':'stop','message':{'content':json.dumps(expected)}}]}).encode()
                try:
                    self.send_response(200); self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
                except (BrokenPipeError,ConnectionResetError): pass
        server = ThreadingHTTPServer(('127.0.0.1',0),Handler)
        thread = threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            llm = LLM({'endpoint':f'http://127.0.0.1:{server.server_port}/v1/chat/completions','model':'test','response_format':'json_schema'})
            actual = await llm.decide(team,[],time.monotonic()+3)
            self.assertEqual(actual.plan,expected)
            self.assertEqual(captured[0]['response_format']['type'],'json_schema')
            llm.config['endpoint'] = f'http://127.0.0.1:{server.server_port}/slow'
            start = time.monotonic()
            with self.assertRaises(TimeoutError): await llm.decide(team,[],start+.2)
            self.assertLess(time.monotonic()-start,.8)
        finally:
            await asyncio.to_thread(server.shutdown)
            server.server_close();thread.join()

    async def test_plugin_that_never_reads_is_killed(self):
        import sys
        plugin = ProcessPlugin({'id':'blocked','version':'v1','modes':[1],
                                'command':[sys.executable,'-c','import time; time.sleep(60)']})
        start=time.monotonic()
        with self.assertRaises(TimeoutError):
            await plugin.decide(fixture(),[{'text':'x'*1000000}],start+.1)
        self.assertLess(time.monotonic()-start,1.)


if __name__ == '__main__': unittest.main()
