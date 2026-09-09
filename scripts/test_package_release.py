import hashlib
import pathlib
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


class PackagingTest(unittest.TestCase):
    def test_release_has_pinned_binaries_with_checksums(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / 'dist').mkdir()
            manifest = (ROOT / 'provider.yaml').read_text()
            (root / 'provider.yaml').write_text(manifest)
            import re
            version = re.search(r'^version: (.+)$', manifest, re.M)[1]
            for name in re.findall(r'/([^/\n]+)$', manifest, re.M):
                if name.startswith('harvester-provider-'):
                    (root / 'dist' / name).write_bytes(b'synthetic packaging fixture: ' + name.encode())
            result = subprocess.run(['python3', str(ROOT / 'scripts/package_release.py'), 'v' + version], cwd=root, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            text = (root / 'dist/provider.yaml').read_text()
            pairs = re.findall(r'path: (.+)\n      checksum: (\w+)', text)
            self.assertEqual(len(pairs), 4)
            for url, checksum in pairs:
                self.assertIn('/releases/download/v' + version + '/', url)
                self.assertEqual(checksum, hashlib.sha256((root / 'dist' / url.rsplit('/', 1)[1]).read_bytes()).hexdigest())
            result = subprocess.run(['sha256sum', '-c', 'checksums.txt'], cwd=root / 'dist', capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
