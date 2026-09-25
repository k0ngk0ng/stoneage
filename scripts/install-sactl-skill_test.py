#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import tempfile

root = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('installer', root / 'scripts/install-sactl-skill.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
with tempfile.TemporaryDirectory(dir=root / 'build', prefix='skill-install-') as temporary:
    base = Path(temporary)
    source = module.bundled_source()
    destinations = module.targets('all', 'project', base / 'project', base / 'home')
    assert destinations == [base / 'project/.agents/skills/sactl', base / 'project/.claude/skills/sactl']
    assert module.targets('all', 'user', base / 'project', base / 'home') == [base / 'home/.agents/skills/sactl', base / 'home/.claude/skills/sactl']
    module.install(source, destinations, dry_run=True)
    assert not (base / 'project').exists()
    module.install(source, destinations)
    for destination in destinations:
        assert (destination / 'SKILL.md').read_bytes() == (source / 'SKILL.md').read_bytes()
        assert (destination / 'references/session.md').is_file()
    module.install(source, destinations)  # Idempotent.
    edited = destinations[1] / 'SKILL.md'
    edited.write_text('user customization')
    try:
        module.install(source, destinations)
        raise AssertionError('overwrote customization')
    except ValueError:
        assert edited.read_text() == 'user customization'
    extra = destinations[1] / 'notes.txt'
    extra.write_text('keep')
    module.install(source, destinations, force=True)
    assert extra.read_text() == 'keep'
    target = base / 'link'
    target.symlink_to(destinations[0], target_is_directory=True)
    try:
        module.install(source, [target], force=True)
        raise AssertionError('followed destination symlink')
    except ValueError:
        pass
print('Standard skill paths, dry run, install, overwrite protection and symlinks passed')
