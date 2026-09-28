"""Private bounded HTTP worker. Secrets arrive on stdin, never argv or logs."""
import json
import sys

from .strategies import LLM


def main():
    try:
        message = json.load(sys.stdin)
        client = LLM(message['config'])
        client.key = message['key']
        plan = client.request(message['payload'],message['timeout'])
        print(json.dumps({'ok':True,'plan':plan}))
    except Exception:
        print('{"ok":false}')


if __name__ == '__main__': main()
