#!/usr/bin/env python3
"""Verify Windows startup and failure handling without real Docker or credentials."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


@unittest.skipUnless(os.name == 'nt', 'requires Windows')
class WindowsStartupTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='qq tavern [test] \u4e2d\u6587 ')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'scripts').mkdir()
        for name in ('start.ps1', 'start.cmd'):
            shutil.copy(Path(__file__).with_name(name), self.root / 'scripts' / name)
        (self.root / '.env').touch()
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        docker = self.bin / 'docker.py'
        docker.write_text('''import json, os, sys
from pathlib import Path
calls = Path(os.environ['CALLS'])
with calls.open('a', encoding='utf-8') as f:
    f.write(json.dumps({'args': sys.argv[1:], 'cwd': os.getcwd()}) + '\\n')
print('Docker diagnostic output', file=sys.stderr)
if len(calls.read_text(encoding='utf-8').splitlines()) == int(os.environ['FAIL_AT']):
    sys.exit(17)
''', encoding='utf-8')
        # ASCII batch content; expand Unicode paths from the environment in cmd.exe.
        (self.bin / 'docker.cmd').write_text(
            '@echo off\n"%TEST_PYTHON%" "%TEST_DOCKER%" %*\nexit /b %errorlevel%\n',
            encoding='ascii')
        self.calls = self.root / 'calls.jsonl'
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ['PATH'],
                        CALLS=str(self.calls), TEST_PYTHON=sys.executable, TEST_DOCKER=str(docker))
        self.powershell = os.environ.get('TEST_POWERSHELL', 'powershell.exe')

    def run_script(self, *args, fail_at=0, stdin=None, wrapper=False):
        self.calls.unlink(missing_ok=True)
        script = self.root / 'scripts' / ('start.cmd' if wrapper else 'start.ps1')
        command = ([str(script)] if wrapper else
                   [self.powershell, '-NoLogo', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', str(script)])
        result = subprocess.run(command + list(args), cwd=self.root.parent,
                                env=dict(self.env, FAIL_AT=str(fail_at)), input=stdin,
                                text=True, encoding='utf-8', errors='replace', capture_output=True,
                                timeout=30)
        entries = [json.loads(line) for line in self.calls.read_text(encoding='utf-8').splitlines()] if self.calls.exists() else []
        for entry in entries:
            self.assertTrue(Path(entry['cwd']).samefile(self.root))
        return result, [entry['args'] for entry in entries]

    def test_manual(self):
        result, calls = self.run_script('manual')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any('qq-login' in c or 'compose.qr.yaml' in c for c in calls))
        self.assertEqual([c[-1] for c in calls], ['--quiet', 'check', 'check', 'worker', 'ps'])
        self.assertIn('ROLE=gateway', calls[1])
        self.assertIn('ROLE=worker', calls[2])

    def test_qr_and_rebind(self):
        for args in [('qr',), ('qr', '--rebind')]:
            with self.subTest(args=args):
                result, calls = self.run_script(*args)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue(all(c[:5] == ['compose', '-f', 'compose.yaml', '-f', 'compose.qr.yaml'] for c in calls))
                self.assertEqual(calls[1][5:], ['stop', 'gateway', 'worker'])
                self.assertEqual(calls[2][-1], 'qq-login')
                self.assertEqual('QQ_LOGIN_FORCE=true' in calls[2], '--rebind' in args)
                self.assertEqual([c[-1] for c in calls[3:]], ['check', 'check', 'worker', 'ps'])

    def test_every_docker_failure_stops_startup(self):
        for mode, count in [('manual', 5), ('qr', 7)]:
            for fail_at in range(1, count + 1):
                with self.subTest(mode=mode, fail_at=fail_at):
                    result, calls = self.run_script(mode, fail_at=fail_at)
                    self.assertEqual(result.returncode, 17, result.stderr)
                    self.assertEqual(len(calls), fail_at)

    def test_prompt(self):
        for choice, qr in [('1', True), ('2', False)]:
            with self.subTest(choice=choice):
                result, calls = self.run_script(stdin=choice + '\n')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(any('qq-login' in c for c in calls), qr)
        result, calls = self.run_script(stdin='invalid\n')
        self.assertEqual(result.returncode, 2)
        self.assertFalse(calls)

    def test_invalid_arguments(self):
        for args in [('manual', '--rebind'), ('unknown',), ('qr', '--unknown'), ('qr', '--rebind', 'extra')]:
            with self.subTest(args=args):
                result, calls = self.run_script(*args)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertFalse(calls)

    def test_missing_env(self):
        (self.root / '.env').unlink()
        result, calls = self.run_script('qr')
        self.assertEqual(result.returncode, 1)
        self.assertIn('.env', result.stderr)
        self.assertFalse(calls)

    def test_missing_docker(self):
        self.env['PATH'] = str(self.bin)
        (self.bin / 'docker.cmd').unlink()
        self.powershell = shutil.which(self.powershell)
        result, calls = self.run_script('qr')
        self.assertEqual(result.returncode, 1)
        self.assertIn('Docker was not found', result.stderr)
        self.assertFalse(calls)

    def test_cmd_wrapper(self):
        result, calls = self.run_script('qr', '--rebind', wrapper=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('QQ_LOGIN_FORCE=true', calls[2])
        result, calls = self.run_script('qr', fail_at=3, wrapper=True)
        self.assertEqual(result.returncode, 17, result.stderr)
        self.assertEqual(len(calls), 3)


if __name__ == '__main__':
    unittest.main()
