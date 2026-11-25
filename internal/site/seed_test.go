package site

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestASeedArchiveUnpacksToThePayload unpacks the archive with the
// system's cpio: the files a guest reads hold exactly the payload.
func TestASeedArchiveUnpacksToThePayload(t *testing.T) {
	payload := &Payload{Salt: bytes.Repeat([]byte{7}, 32), Data: []byte(`{"userData":"#cloud-config\nhostname: example\n","sshAuthorizedKeys":"ssh-ed25519 AAAAExampleOnly\n"}`)}
	archive, err := SeedArchive(payload, "fleet-0123")
	if err != nil {
		t.Fatal(err)
	}
	if len(archive)%4 != 0 || !bytes.Contains(archive, []byte("TRAILER!!!")) {
		t.Fatal("the archive is not a padded newc archive")
	}
	cpio, err := exec.LookPath("cpio")
	if err != nil {
		t.Fatal("cpio is required to check the archive")
	}
	dir := t.TempDir()
	command := exec.Command(cpio, "-id", "--quiet")
	command.Dir, command.Stdin = dir, bytes.NewReader(archive)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cpio: %v: %s", err, output)
	}
	for name, want := range map[string]string{
		"salt": string(payload.Salt), "data": string(payload.Data),
		"user-data": "#cloud-config\nhostname: example\n", "meta-data": "instance-id: fleet-0123\n",
		"authorized_keys": "ssh-ed25519 AAAAExampleOnly\n",
	} {
		got, err := os.ReadFile(filepath.Join(dir, SeedDirectory, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
	if _, err := SeedArchive(payload, "bad\nid"); err == nil {
		t.Fatal("an instance ID with a newline was accepted")
	}
}
