package cli

import (
	"syscall"
	"unsafe"
)

func hostKeychain() Keychain { return CredentialManager{} }

var (
	advapi32     = syscall.NewLazyDLL("advapi32.dll")
	procCredRead = advapi32.NewProc("CredReadW")
	procCredFree = advapi32.NewProc("CredFree")
)

// credTypeGeneric is CRED_TYPE_GENERIC: what `cmdkey /generic:` writes.
const credTypeGeneric = 1

// credential is CREDENTIALW, field for field (wincred.h). Only the blob is
// read; the rest is here because the layout is.
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

func credReadAvailable() bool { return procCredRead.Find() == nil && procCredFree.Find() == nil }

// credRead returns a generic credential's blob, copied out of the buffer
// Windows allocated before that buffer is freed. Any failure - above all
// ERROR_NOT_FOUND, the state of a fresh machine - is "no entry".
func credRead(target string) ([]byte, bool) {
	name, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return nil, false
	}
	var cred *credential
	ok, _, _ := procCredRead.Call(
		uintptr(unsafe.Pointer(name)),
		credTypeGeneric,
		0, // reserved
		uintptr(unsafe.Pointer(&cred)),
	)
	if ok == 0 || cred == nil {
		return nil, false
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))

	if cred.CredentialBlob == nil || cred.CredentialBlobSize == 0 {
		return nil, false
	}
	blob := make([]byte, cred.CredentialBlobSize)
	copy(blob, unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize))
	return blob, true
}
