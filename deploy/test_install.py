"""Exercise the actual installer in temporary directories with mocked host services."""
import hashlib
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
BASH = os.environ.get("TUNNELX_TEST_BASH") or shutil.which("bash")


@unittest.skipUnless(BASH, "bash is required")
class InstallTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="tunnelx-install-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.config = self.root / "config"
        self.data = self.root / "data"
        self.units = self.root / "units"
        self.backups = self.root / "backups"
        self.mocks = self.root / "mocks"
        for path in (self.bin, self.units, self.mocks):
            path.mkdir()
        script = (ROOT / "deploy/install.sh").read_text(encoding="utf-8")
        for old, new in {
            "/usr/local/bin": self.bin,
            "/etc/tunnel-server": self.config,
            "/var/lib/tunnel-server": self.data,
            "/etc/systemd/system": self.units,
            "/var/backups/tunnel-server": self.backups,
        }.items():
            script = script.replace(old, new.as_posix())
        # Only the copied test script bypasses the root check; host commands are mocked.
        script = script.replace('[[ $EUID -eq 0 ]] || die "请用 root 运行：sudo $0 $*"', ':')
        mock_path = self.mocks.as_posix()
        if os.name == "nt":
            mock_path = "/" + mock_path[0].lower() + mock_path[2:]
        script = script.replace("set -euo pipefail", 'export PATH="' + mock_path + ':$PATH"\nset -euo pipefail', 1)
        self.script = self.root / "install.sh"
        self.script.write_text(script, encoding="utf-8", newline="\n")
        self.env = dict(os.environ, PATH=str(self.mocks) + os.pathsep + os.environ["PATH"], TEST_ROOT=self.root.as_posix())
        self.command("systemctl", r'''printf '%s\n' "$*" >> "$TEST_ROOT/systemctl.log"
case "$1" in
  is-active) test -f "$TEST_ROOT/active" ;;
  stop) rm -f "$TEST_ROOT/active" ;;
  start) touch "$TEST_ROOT/active" ;;
  list-unit-files) echo 'tunnel-server.service enabled' ;;
  *) exit 0 ;;
esac
''')
        for name in ("id", "useradd", "userdel", "chown", "sleep"):
            self.command(name, "exit 0\n")
        for name in ("ufw", "firewall-cmd"):
            self.command(name, "exit 1\n")
        self.command("file", "echo x86-64\n")
        self.command("ssh-keygen", r'''if [[ $1 == -lf ]]; then echo '256 SHA256:fixture'; exit; fi
while (($#)); do
  if [[ $1 == -f ]]; then key=$2; shift; fi
  shift
done
printf 'host-identity\n' > "$key"
printf 'host-public\n' > "$key.pub"
''')
        self.source = self.root / "tunnel-server"
        self.source.write_text('#!/usr/bin/env bash\ncase "$1" in\n-version) echo "tunnel-server test" ;;\n-check-auth) exit "${FAIL_AUTH:-0}" ;;\n*) exit 1 ;;\nesac\n', newline="\n")
        self.source.chmod(0o755)

    def command(self, name, body):
        path = self.mocks / name
        path.write_text("#!/usr/bin/env bash\n" + body, newline="\n")
        path.chmod(0o755)

    def install(self, *args, success=True):
        result = subprocess.run([BASH, str(self.script), "--binary", "tunnel-server", *args], env=self.env, text=True, encoding="utf-8", errors="replace", capture_output=True)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def digest(self, path):
        return hashlib.sha256(path.read_bytes()).hexdigest()

    def test_fresh_and_repeated_installs_preserve_credentials_and_database(self):
        self.install("--port", "3333")
        auth = self.data / "authorized_keys"
        auth.write_text("ssh-ed25519 verified-public-key device\n")
        db = self.data / "tunnel-server.db"
        with sqlite3.connect(db) as conn:
            conn.execute("CREATE TABLE identities(device_id TEXT, fingerprint TEXT)")
            conn.execute("INSERT INTO identities VALUES('verified-device','SHA256:verified')")
        conn.close()
        files = (auth, db, self.config / "host_key")
        expected = [self.digest(path) for path in files]
        for running in (False, True):
            active = self.root / "active"
            if running:
                active.touch()
            else:
                active.unlink(missing_ok=True)
            self.install("--port", "3333")
            self.assertEqual([self.digest(path) for path in files], expected)
            self.assertIn("-addr :3333", (self.units / "tunnel-server.service").read_text(encoding="utf-8"))
        backups = list(self.backups.iterdir())
        self.assertEqual(len(backups), 2)
        for backup in backups:
            self.assertEqual(self.digest(backup / "data/authorized_keys"), expected[0])
            self.assertEqual(self.digest(backup / "data/tunnel-server.db"), expected[1])
            self.assertEqual(self.digest(backup / "config/host_key"), expected[2])
        self.assertNotIn("-admin-account", (self.root / "systemctl.log").read_text(encoding="utf-8"))

    def test_missing_credentials_and_invalid_auth_stop_before_replacement(self):
        self.install()
        (self.data / "tunnel-server.db").write_bytes(b"existing database")
        binary = self.bin / "tunnel-server"
        expected = self.digest(binary)
        log = self.root / "systemctl.log"
        before = log.read_text(encoding="utf-8")
        self.env["FAIL_AUTH"] = "1"
        self.install(success=False)
        self.assertEqual(self.digest(binary), expected)
        self.assertEqual(log.read_text(encoding="utf-8"), before)
        del self.env["FAIL_AUTH"]
        (self.config / "host_key").unlink()
        self.install(success=False)
        self.assertFalse((self.config / "host_key").exists())
        self.assertEqual(self.digest(binary), expected)
        self.assertEqual(log.read_text(encoding="utf-8"), before)


if __name__ == "__main__":
    unittest.main()
