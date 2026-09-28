"""Mac-friendly isolated native collection using an already installed image."""
import argparse
import os
from pathlib import Path
import subprocess


def main(arguments=None):
    parser=argparse.ArgumentParser(description='Collect real native 1v1–5v5 matches through sactl in an isolated container')
    parser.add_argument('--work',required=True,help='new directory under repository build/')
    parser.add_argument('--mode',type=int,choices=range(1,6),default=1)
    parser.add_argument('--matches',type=int,default=2)
    parser.add_argument('--strategy',choices=('basic','learned','explore'),default='basic')
    parser.add_argument('--model')
    parser.add_argument('--reconnect',action='store_true')
    parser.add_argument('--seed',type=int,default=1)
    parser.add_argument('--image',default='gcc:13-bookworm',help='already installed image with Python 3.11 and compatible libc; never pulled')
    args=parser.parse_args(arguments)
    root=Path(__file__).resolve().parents[2]
    def inside(value):
        path=Path(value).expanduser()
        path=(path if path.is_absolute() else root/path).resolve()
        relative=path.relative_to(root/'build')
        return path,'/repo/build/'+str(relative)
    try:
        work,container_work=inside(args.work)
        model=inside(args.model)[1] if args.model else None
    except ValueError:
        parser.error('work and model must be inside repository build/')
    if work.exists(): parser.error('work must be a new directory; existing evidence is never overwritten')
    if args.matches<1: parser.error('matches must be positive')
    if args.strategy=='learned' and not model: parser.error('learned requires --model')
    binaries={'sactl':'bin/sactl','gateway':'bin/stoneage-gateway','seed':'bin/seed-network',
              'gmsv':'native/gmsv/gmsvjt.exe','saac':'native/saac/saacjt.exe'}
    for key,value in binaries.items():
        if not (root/'build/local-arena'/value).is_file():
            parser.error(f'missing native {key} binary: build/local-arena/{value}; see docs/local-arena-agent.md')
    work.parent.mkdir(parents=True,exist_ok=True)
    command=['docker','run','--rm','--pull','never','--network','none','--read-only',
             '--cpus','2','--memory','2g','--tmpfs','/tmp:rw,size=128m',
             '--mount',f'type=bind,src={root},dst=/repo,readonly',
             '--mount',f'type=bind,src={root / "build"},dst=/repo/build',
             '-e','PYTHONDONTWRITEBYTECODE=1','--entrypoint','python3',args.image,
             '/repo/tools/arena_agent/tests/native_ladder.py',
             '--work',container_work,'--mode',str(args.mode),'--matches',str(args.matches),
             '--strategy',args.strategy,'--policy-seed',str(args.seed),'--timeout',str(max(180,args.matches*120))]
    for key,value in binaries.items(): command += ['--'+key,'/repo/build/local-arena/'+value]
    if model: command += ['--model',model]
    if args.reconnect: command += ['--reconnect']
    raise SystemExit(subprocess.call(command,cwd=root))
