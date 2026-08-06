// Package credentials is the encrypted credential store, byte-compatible with
// the Python build's credentials.py — it must read a file that application
// already wrote, and write one that application can still read.
//
// On the strength of this. The passphrase is a constant in this source file,
// so anyone holding the application can derive the key and read the file. That
// makes this obfuscation, not secrecy: it stops a key being read by accident —
// over someone's shoulder, in a backup, in a screen share — but it does not
// stop anyone who wants it. The macOS Keychain, which is protected by the login
// password and by the OS, is still supported and is the stronger option.
//
// The file is written 0600 and never logged.
package credentials

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/fernet/fernet-go"
	"golang.org/x/crypto/pbkdf2"

	"github.com/dennis2lee/live-translation/src_go/internal/platform"
)

// Filename of the store inside the application-support directory.
const Filename = "credentials.enc"

// These three values define the file format. They match credentials.py
// exactly; changing any of them orphans every store already written.
var (
	passphrase = []byte("praykepc")
	salt       = []byte("livecap-credential-store-v1")
)

const iterations = 200_000

// StorePath is where the store lives.
func StorePath() string {
	return filepath.Join(platform.AppSupportDir(), Filename)
}

// dirOverride lets tests redirect the store, the way tests/conftest.py does for
// the Python build. Never set outside tests.
var dirOverride string

func storePath() string {
	if dirOverride != "" {
		return filepath.Join(dirOverride, Filename)
	}
	return StorePath()
}

// key derives the Fernet key. Python builds it as
// base64.urlsafe_b64encode(pbkdf2_hmac("sha256", passphrase, salt, 200000)),
// and Fernet's own constructor then base64-decodes that — so the Go side
// decodes the same base64 text to reach the same 32 bytes.
func key() (*fernet.Key, error) {
	derived := pbkdf2.Key(passphrase, salt, iterations, 32, sha256.New)
	return fernet.DecodeKey(base64.URLEncoding.EncodeToString(derived))
}

// LoadAll returns every stored value. An unreadable or tampered file reads as
// empty, exactly as the Python version does — a corrupt store must never stop
// the application starting.
func LoadAll() map[string]string {
	blob, err := os.ReadFile(storePath())
	if err != nil {
		return map[string]string{}
	}
	k, err := key()
	if err != nil {
		return map[string]string{}
	}
	raw := fernet.VerifyAndDecrypt(blob, 0, []*fernet.Key{k})
	if raw == nil {
		return map[string]string{}
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return map[string]string{}
	}
	if values == nil {
		return map[string]string{}
	}
	return values
}

// SaveAll writes the whole store. Returns an error rather than panicking.
func SaveAll(values map[string]string) error {
	path := storePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	k, err := key()
	if err != nil {
		return err
	}
	blob, err := fernet.EncryptAndSign(raw, k)
	if err != nil {
		return err
	}
	// Write then rename, so an interrupted write cannot truncate the store.
	temp := path + ".tmp"
	if err := os.WriteFile(temp, blob, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// Get returns one value, or "" when it is absent or blank.
func Get(name string) string {
	return strings.TrimSpace(LoadAll()[name])
}

// Put stores one value.
func Put(name, value string) error {
	values := LoadAll()
	values[name] = strings.TrimSpace(value)
	return SaveAll(values)
}

// Remove deletes one value. An empty store is just a missing file.
func Remove(name string) error {
	values := LoadAll()
	if _, ok := values[name]; !ok {
		return nil
	}
	delete(values, name)
	if len(values) == 0 {
		err := os.Remove(storePath())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return SaveAll(values)
}
