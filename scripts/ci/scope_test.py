import unittest
from unittest.mock import patch
from pathlib import Path
from tempfile import TemporaryDirectory

from scope import event_scope, gui_packages, scope


class ScopeTests(unittest.TestCase):
    def test_documentation_needs_no_product_build(self):
        self.assertFalse(any(scope(['README.md', 'docs/architecture.svg']).values()))

    def test_source_runs_tests_without_repackaging(self):
        self.assertEqual(scope(['internal/agent/controller/controller.go']), {
            'code': True, 'gui': False, 'macos_native': True, 'windows_native': True,
            'macos_package': False, 'windows_package': False})

    def test_gui_changes_do_not_repeat_native_backend_tests(self):
        result = scope(['internal/gui/page_dashboard.go'])
        self.assertTrue(result['gui'])
        self.assertFalse(result['macos_native'])
        self.assertFalse(result['windows_native'])

    def test_platform_sources_select_their_native_runner(self):
        for path in ['native/macos/ServiceBridge/Sources/XPC.swift',
                     'cmd/agentd/daemon_darwin.go', 'internal/darwin/bridge/bridge.go']:
            self.assertTrue(scope([path])['macos_native'])
            self.assertFalse(scope([path])['windows_native'])
        self.assertTrue(scope(['internal/winsys/acl_windows.go'])['windows_native'])
        self.assertFalse(scope(['internal/winsys/acl_windows.go'])['macos_native'])

    def test_gui_dependencies_follow_new_imports_including_test_files(self):
        with TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'go.mod').write_text('module example\n')
            for name, body in {
                'gui/gui.go': 'package gui\nimport "example/internal/model"',
                'gui/gui_test.go': 'package gui\nimport `example/internal/fixture`',
                'model/model.go': 'package model\nimport "example/internal/store"',
                'model/model_test.go': 'package model\nimport "example/internal/unrelated"',
                'store/store.go': 'package store',
                'fixture/data.go': 'package fixture',
            }.items():
                source = root / 'internal' / name
                source.parent.mkdir(parents=True, exist_ok=True)
                source.write_text(body)
            self.assertEqual(gui_packages(root), {
                'internal/gui', 'internal/model', 'internal/store', 'internal/fixture'})

    def test_platform_installer_changes_are_independent(self):
        self.assertTrue(scope(['packaging/macos/build.sh'])['macos_package'])
        self.assertFalse(scope(['packaging/macos/build.sh'])['windows_package'])
        self.assertTrue(scope(['packaging/windows/installer.nsi'])['windows_package'])
        self.assertFalse(scope(['packaging/windows/installer.nsi'])['macos_package'])

    def test_shared_build_inputs_rebuild_both_installers(self):
        for path in ['go.mod', 'mygo.json', 'resources/icon.png',
                     'internal/brand/brand.go', '.github/actions/go/action.yml',
                     'scripts/ci/scope.py', 'scripts/notices/main.go']:
            with self.subTest(path=path):
                result = scope([path])
                self.assertTrue(result['code'])
                self.assertTrue(result['macos_package'])
                self.assertTrue(result['windows_package'])

    def test_missing_diff_is_conservative(self):
        self.assertTrue(all(event_scope('push', {'before': '0' * 40}).values()))
        with patch('scope.subprocess.run') as run:
            run.return_value.returncode = 1
            self.assertTrue(all(event_scope('push', {'before': 'missing'}).values()))

    def test_schedule_runs_no_builds(self):
        self.assertFalse(any(event_scope('schedule', {}).values()))

    def test_manual_run_can_omit_installers(self):
        self.assertTrue(all(event_scope('workflow_dispatch', {}).values()))
        self.assertEqual(event_scope('workflow_dispatch', {'inputs': {'installers': 'false'}}), {
            'code': True, 'gui': True, 'macos_native': True, 'windows_native': True,
            'macos_package': False, 'windows_package': False})


if __name__ == '__main__':
    unittest.main()
