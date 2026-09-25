#!/usr/bin/env python3
"""Install the bundled, vendor-neutral sactl skill into standard agent paths."""
import argparse
from pathlib import Path
import shutil
import sys


def bundled_source():
    script = Path(__file__).resolve()
    for source in (script.parent / 'skills' / 'sactl',
                   script.parent.parent / '.agents' / 'skills' / 'sactl'):
        if (source / 'SKILL.md').is_file():
            return source
    raise ValueError('Bundled skills/sactl/SKILL.md not found; use the source checkout or a complete sactl release archive')


def targets(agent, scope, project, home):
    base = home if scope == 'user' else project
    agents = ('codex', 'claude') if agent == 'all' else (agent,)
    return [base / ('.agents' if item == 'codex' else '.claude') / 'skills' / 'sactl'
            for item in agents]


def install(source, destinations, force=False, dry_run=False):
    files = sorted(p for p in source.rglob('*') if p.is_file())
    # Preflight every destination before writing either agent's files.
    for destination in destinations:
        for ancestor in (destination, *destination.parents):
            if ancestor.is_symlink():
                raise ValueError(f'Refusing symlink destination: {ancestor}')
            if ancestor.exists() and not ancestor.is_dir():
                raise ValueError(f'Not a directory: {ancestor}')
        if source.resolve() == destination.resolve():
            continue  # Already discoverable in this source checkout.
        if destination.exists() and not destination.is_dir():
            raise ValueError(f'Not a directory: {destination}')
        for path in files:
            target = destination / path.relative_to(source)
            for ancestor in (target, *target.parents):
                if ancestor.is_symlink():
                    raise ValueError(f'Refusing symlink destination: {ancestor}')
            if target.exists() and (not target.is_file() or (target.read_bytes() != path.read_bytes() and not force)):
                raise ValueError(f'Existing file differs: {target}; review it before using --force')
    for destination in destinations:
        if source.resolve() == destination.resolve():
            print(f'Already installed: {destination}')
            continue
        print(f'{"Would install" if dry_run else "Installing"}: {destination}')
        if dry_run:
            continue
        for path in files:
            target = destination / path.relative_to(source)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)
    # Existing unrelated files and agent configuration are never removed.


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--agent', choices=['codex', 'claude', 'all'], required=True)
    parser.add_argument('--scope', choices=['project', 'user'], default='project')
    parser.add_argument('--project-dir', type=Path, default=Path.cwd())
    parser.add_argument('--dry-run', action='store_true')
    parser.add_argument('--force', action='store_true', help='replace differing skill files, preserving unrelated files')
    args = parser.parse_args()
    try:
        install(bundled_source(), targets(args.agent, args.scope, args.project_dir.absolute(), Path.home()),
                force=args.force, dry_run=args.dry_run)
    except (ValueError, OSError) as error:
        print(f'Cannot install sactl skill: {error}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
