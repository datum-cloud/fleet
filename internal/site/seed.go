package site

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SeedDirectory is where the seed archive puts a payload in the installer's
// root filesystem. The installer copies it to the target disk, and the
// installed system's first boot reads it.
const SeedDirectory = "fleet-bootstrap"

// BootstrapMarker starts the console line in which the installed system,
// on its first disk boot, reports the digest of the bootstrap data it
// found: "FLEET-BOOTSTRAP:<receipt>:sha256:<hex>". The guest recomputes it
// from the salt and data on its own disk, so the line proves receipt
// without printing the data or an unsalted hash of it.
const BootstrapMarker = "FLEET-BOOTSTRAP:"

// BootstrapLine is the console line a guest prints for a receipt and digest.
func BootstrapLine(receipt, digest string) string {
	return BootstrapMarker + receipt + ":" + digest
}

// SeedArchive renders a verified payload as the newc cpio archive a network
// boot loads as a second initrd. It holds the salt and data the guest
// digests, and the NoCloud user-data, meta-data and authorized keys the
// installed system reads. Every file is readable by root only.
func SeedArchive(payload *Payload, instanceID string) ([]byte, error) {
	if payload == nil || instanceID == "" || strings.ContainsAny(instanceID, "\n:") {
		return nil, errors.New("site: a seed needs a payload and an instance ID")
	}
	var values struct {
		UserData          string `json:"userData"`
		SSHAuthorizedKeys string `json:"sshAuthorizedKeys"`
	}
	if err := json.Unmarshal(payload.Data, &values); err != nil {
		return nil, err
	}
	var archive bytes.Buffer
	files := []struct {
		name string
		mode uint32
		data []byte
	}{
		{SeedDirectory, 0o040700, nil},
		{SeedDirectory + "/salt", 0o100600, payload.Salt},
		{SeedDirectory + "/data", 0o100600, payload.Data},
		{SeedDirectory + "/user-data", 0o100600, []byte(values.UserData)},
		{SeedDirectory + "/meta-data", 0o100600, []byte("instance-id: " + instanceID + "\n")},
		{SeedDirectory + "/authorized_keys", 0o100600, []byte(values.SSHAuthorizedKeys)},
	}
	for index, file := range files {
		writeEntry(&archive, uint32(index+1), file.mode, file.name, file.data)
	}
	writeEntry(&archive, 0, 0, "TRAILER!!!", nil)
	return archive.Bytes(), nil
}

// writeEntry writes one newc cpio entry, padding the header and name, and
// the data, to four bytes.
func writeEntry(out *bytes.Buffer, inode, mode uint32, name string, data []byte) {
	nlink := uint32(1)
	if mode&0o040000 != 0 {
		nlink = 2
	}
	fmt.Fprintf(out, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X", inode, mode, 0, 0, nlink, 0, len(data), 0, 0, 0, 0, len(name)+1, 0)
	out.WriteString(name)
	out.WriteByte(0)
	pad(out)
	out.Write(data)
	pad(out)
}

func pad(out *bytes.Buffer) {
	for out.Len()%4 != 0 {
		out.WriteByte(0)
	}
}
