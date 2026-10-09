"""Static/loopback fixtures only: never invoke install_main or privileged operations."""
from pathlib import Path
import functools
import hashlib
import http.server
import os
import shutil
import subprocess
import tempfile
import threading
import unittest

DEPLOY = Path(__file__).resolve().parent

class InstallerSecurityTests(unittest.TestCase):
    def bash(self, commands, script='install-virtualis.sh'):
        path = (DEPLOY/script).as_posix()
        result = subprocess.run(['bash', '-c', 'source "$1"; ' + commands, 'fixture', path], capture_output=True, text=True)
        return result

    def test_master_download_requires_sha256(self):
        script = (DEPLOY/'install-virtualis.sh').read_text(encoding='utf-8')
        for value in ['verify_sha256', 'SHA256SUMS', '--expected-sha256']: self.assertIn(value, script)
        self.assertNotIn('|| pnpm install', script)

    def test_digest_matching_and_mismatch(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
            file = Path(tmp)/'fixture.bin'; file.write_bytes(b'\x7fELF' + b'fixture' * 200)
            expected = hashlib.sha256(file.read_bytes()).hexdigest()
            for digest, accepted in [(expected, True), ('0'*64, False), ('invalid', False)]:
                p=self.bash('verify_sha256 "'+file.as_posix()+'" '+digest)
                self.assertEqual(p.returncode == 0, accepted, p.stderr)

    def test_manifest_rejects_missing_duplicate_malformed(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
            file=Path(tmp)/'SHA256SUMS'; digest='a'*64
            cases=[(digest+'  agent\n',True),(digest+'  other\n',False),(digest+'  agent\n'+digest+'  agent\n',False),('not-a-digest  agent\n',False)]
            for text, accepted in cases:
                file.write_text(text,encoding='ascii')
                p=self.bash('checksum_for "'+file.as_posix()+'" agent')
                self.assertEqual(p.returncode == 0, accepted, p.stderr)
            file.unlink()
            self.assertNotEqual(self.bash('checksum_for "'+file.as_posix()+'" agent').returncode,0)

    def test_pe_reads_exactly_two_bytes(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
            file=Path(tmp)/'fixture.exe'; file.write_bytes(b'MZ\x90\x00')
            self.assertEqual(self.bash('verify_binary_magic "'+file.as_posix()+'" windows').returncode,0)
            file.write_bytes(b'NO\x90\x00')
            self.assertNotEqual(self.bash('verify_binary_magic "'+file.as_posix()+'" windows').returncode,0)

    def test_mode_contract_and_rejections(self):
        for mode in ['1','2','4']:
            self.assertEqual(self.bash('validate_mode '+mode).returncode,0)
        for mode in ['3','0','5','unknown']:
            p=self.bash('validate_mode '+mode)
            self.assertNotEqual(p.returncode,0)
        self.assertIn('LXD-compatible client', (DEPLOY/'install-agent.sh').read_text())

    def test_http_opt_in_and_token_service_arguments(self):
        self.assertNotEqual(self.bash('check_url http://127.0.0.1:1').returncode,0)
        self.assertEqual(self.bash('ALLOW_INSECURE=1; check_url http://127.0.0.1:1').returncode,0)
        script=(DEPLOY/'install-agent.sh').read_text()
        self.assertIn('--token-file $DEST/token',script)
        self.assertIn('install -o root -m 0600 "$TOKEN_FILE"',script)
        self.assertNotIn('--token $TOKEN',script)
        self.assertIn('User=root',script)

    def test_master_service_isolation(self):
        script=(DEPLOY/'install-virtualis.sh').read_text()
        block=script[script.index('Description=Virtualis Master'):]
        for value in ['User=virtualis','NoNewPrivileges=true','ProtectSystem=strict','PrivateTmp=true','ReadWritePaths=$MASTER_DIR/data']: self.assertIn(value,block)
        self.assertNotIn('User=root',block)

    def test_loopback_download_requires_opt_in_and_expected_digest(self):
        with tempfile.TemporaryDirectory(dir=os.environ.get('TMPDIR')) as tmp:
            fixture=Path(tmp)/'fixture'; fixture.write_bytes(b'fixture-only binary')
            expected=hashlib.sha256(fixture.read_bytes()).hexdigest()
            handler=functools.partial(http.server.SimpleHTTPRequestHandler,directory=tmp)
            server=http.server.ThreadingHTTPServer(('127.0.0.1',0),handler)
            thread=threading.Thread(target=server.serve_forever,daemon=True); thread.start()
            try:
                url='http://127.0.0.1:'+str(server.server_port)+'/fixture'
                out=(Path(tmp)/'download').as_posix()
                self.assertNotEqual(self.bash('download "'+url+'" "'+out+'"').returncode,0)
                p=self.bash('ALLOW_INSECURE=1; download "'+url+'" "'+out+'"; verify_sha256 "'+out+'" '+expected)
                self.assertEqual(p.returncode,0,p.stderr)
                p=self.bash('ALLOW_INSECURE=1; download "'+url+'" "'+out+'"; verify_sha256 "'+out+'" '+'0'*64)
                self.assertNotEqual(p.returncode,0)
            finally: server.shutdown(); server.server_close(); thread.join()

if __name__=='__main__': unittest.main()
