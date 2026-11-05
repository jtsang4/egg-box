#!/usr/bin/env python3
"""Select CI work from the complete tested diff; unknown bases run every gate."""

import json
import os
from pathlib import Path
import re
import subprocess


def gui_packages(root=Path(__file__).resolve().parents[2]):
    """Follow in-repository imports, including tests and all platform variants."""
    try:
        module = next(line.split()[1].strip('"') for line in
                      (root / 'go.mod').read_text().splitlines() if line.startswith('module '))
        imports = re.compile(r'["`]' + re.escape(module + '/') + r'([^"`\r\n]+)["`]')
        pending, visited = ['internal/gui'], set()
        while pending:
            package = pending.pop()
            if package in visited:
                continue
            visited.add(package)
            for source in (root / package).glob('*.go'):
                if package != 'internal/gui' and source.name.endswith('_test.go'):
                    continue
                pending.extend(imports.findall(source.read_text()))
        return visited
    except (OSError, StopIteration, UnicodeError):
        return None


def native_path(path, platform):
    for name, prefixes in {
        'macos': ('native/macos/', 'packaging/macos/', 'internal/darwin/',
                  'internal/macsys/', 'internal/agent/platform/macplat/'),
        'windows': ('packaging/windows/', 'internal/winsys/', 'internal/agent/platform/winplat/'),
    }.items():
        suffix = '_darwin' if name == 'macos' else '_windows'
        if path.startswith(prefixes) or path.endswith((suffix + '.go', suffix + '_test.go')):
            return platform == name
    return not path.startswith(('internal/gui/', 'internal/brandart/', 'resources/', 'tools/mkicon/'))


def scope(paths, *, full=False):
    product = [p for p in paths if not (p.endswith('.md') or p.startswith('docs/'))]
    code = full or bool(product)
    shared = ('go.mod', 'go.sum', 'mygo.json')
    build_dirs = ('.github/', 'scripts/ci/', 'scripts/notices/', 'resources/',
                  'tools/mkicon/', 'internal/brand/')
    build = full or any(p in shared or p.startswith(build_dirs) for p in product)
    all_tests = full or any(p in shared or p.startswith(('.github/', 'scripts/ci/')) for p in product)
    dependencies = gui_packages()
    return {
        'code': code,
        'gui': code and (all_tests or dependencies is None or any(
            p.startswith(tuple(d + '/' for d in dependencies) + ('resources/', 'tools/mkicon/')) for p in product)),
        'macos_native': all_tests or any(native_path(p, 'macos') for p in product),
        'windows_native': all_tests or any(native_path(p, 'windows') for p in product),
        'macos_package': build or any(p.startswith(('packaging/macos/', 'native/macos/')) for p in product),
        'windows_package': build or any(p.startswith('packaging/windows/') for p in product),
    }


def event_scope(event_name, event):
    if event_name == 'schedule':
        return scope([])
    if event_name == 'workflow_dispatch':
        result = scope([], full=True)
        installers = str(event.get('inputs', {}).get('installers', 'true')).lower() == 'true'
        result.update(macos_package=installers, windows_package=installers)
        return result
    base = (event.get('pull_request', {}).get('base', {}).get('sha')
            if event_name == 'pull_request' else event.get('before'))
    if not base or not base.strip('0'):
        return scope([], full=True)
    diff = subprocess.run(['git', 'diff', '--name-only', '-z', base, 'HEAD', '--'],
                          capture_output=True)
    if diff.returncode:
        return scope([], full=True)
    paths = diff.stdout.decode(errors='surrogateescape').strip('\0').split('\0')
    return scope([p for p in paths if p])


if __name__ == '__main__':
    with open(os.environ['GITHUB_EVENT_PATH']) as source:
        result = event_scope(os.environ['GITHUB_EVENT_NAME'], json.load(source))
    with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
        for key, value in result.items():
            print(f'{key}={str(value).lower()}', file=output)
