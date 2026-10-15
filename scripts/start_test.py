#!/usr/bin/env python3
"""Exercise startup ordering using a Docker stand-in, without running containers."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

class StartupTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'scripts').mkdir()
        shutil.copy(Path(__file__).with_name('start.sh'), self.root / 'scripts/start.sh')
        (self.root / '.env').touch()
        docker = self.root / 'docker'
        docker.write_text('''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['CALLS'], 'a') as f: f.write(json.dumps(sys.argv[1:]) + '\\n')
if os.environ.get('FAIL_ARG') in sys.argv: sys.exit(1)
''')
        docker.chmod(0o755)
        self.calls = self.root / 'calls.jsonl'
        self.env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ['PATH'], CALLS=str(self.calls))

    def run_script(self, *args, fail='', stdin=None):
        result = subprocess.run([str(self.root / 'scripts/start.sh'), *args], env=dict(self.env, FAIL_ARG=fail), input=stdin, text=True, capture_output=True)
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()] if self.calls.exists() else []
        return result, calls

    def test_manual(self):
        result, calls = self.run_script('manual')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any('qq-login' in c or 'compose.qr.yaml' in c for c in calls))
        self.assertEqual([c[-1] for c in calls], ['--quiet', 'check', 'check', 'worker', 'ps'])

    def test_qr_rebind_order(self):
        result, calls = self.run_script('qr', '--rebind')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(all('compose.qr.yaml' in c for c in calls))
        self.assertIn('stop', calls[1])
        self.assertIn('QQ_LOGIN_FORCE=true', calls[2])
        self.assertEqual(calls[2][-1], 'qq-login')
        self.assertIn('up', calls[-2])

    def test_failed_scan_stops_startup(self):
        result, calls = self.run_script('qr', fail='qq-login')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any('up' in c or 'check' in c for c in calls))

    def test_failed_config_stops_startup(self):
        result, calls = self.run_script('manual', fail='check')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any('up' in c for c in calls))

    def test_prompt(self):
        result, calls = self.run_script(stdin='1\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any('qq-login' in c for c in calls))

    def test_invalid(self):
        result, calls = self.run_script('manual', '--rebind')
        self.assertEqual(result.returncode, 2)
        self.assertFalse(calls)

    def test_missing_env(self):
        (self.root / '.env').unlink()
        result, calls = self.run_script('qr')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('.env', result.stderr)
        self.assertFalse(calls)

if __name__ == '__main__':
    unittest.main()
