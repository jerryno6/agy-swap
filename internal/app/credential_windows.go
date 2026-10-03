//go:build windows

package app

import (
	"context"
	"encoding/json"
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const credTypeGeneric = 1

// Windows limits a generic credential's CredentialBlob to CRED_MAX_CREDENTIAL_BLOB_SIZE.
const windowsCredentialBlobLimit = 2560

var errWindowsCredentialTooLarge = errors.New("Windows credential exceeds the Credential Manager size limit")

// windowsSessionCredential converts agy-swap's saved go-keyring payload into
// the compact JSON session format consumed by agy. Refresh can leave a newer
// id_token under token while the root id_token is stale; the existing token
// readers prefer the nested value, so promote it before removing the duplicate.
func windowsSessionCredential(value string) (string, error) {
	decoded := decodeToken(value)
	if decoded == nil {
		return "", errors.New("invalid session credential JSON")
	}
	if nested := getMap(decoded["token"]); nested != nil {
		if nestedID := getString(nested, "id_token"); nestedID != "" {
			decoded["id_token"] = nestedID
			delete(nested, "id_token")
		}
	}
	data, err := json.Marshal(decoded)
	if err != nil {
		return "", errors.New("could not encode session credential")
	}
	if len(data) > windowsCredentialBlobLimit {
		return "", errWindowsCredentialTooLarge
	}
	return string(data), nil
}

type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var advapi = windows.NewLazySystemDLL("advapi32.dll")
var procCredRead = advapi.NewProc("CredReadW")
var procCredWrite = advapi.NewProc("CredWriteW")
var procCredDelete = advapi.NewProc("CredDeleteW")
var procCredFree = advapi.NewProc("CredFree")

func (osCredentialBackend) PrepareSession(value string) (string, error) {
	return windowsSessionCredential(value)
}

func platformCredentialGet(context.Context) string {
	target, _ := windows.UTF16PtrFromString("gemini:antigravity")
	var pointer *credential
	ok, _, _ := procCredRead.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&pointer)))
	if ok == 0 || pointer == nil {
		return ""
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(pointer)))
	if pointer.CredentialBlobSize == 0 {
		return ""
	}
	blob := unsafe.Slice(pointer.CredentialBlob, pointer.CredentialBlobSize)
	return string(blob)
}
func platformCredentialSet(_ context.Context, value string) bool {
	blob := []byte(value)
	if len(blob) == 0 || len(blob) > windowsCredentialBlobLimit {
		return false
	}
	target, _ := windows.UTF16PtrFromString("gemini:antigravity")
	user, _ := windows.UTF16PtrFromString("antigravity")
	item := credential{Type: credTypeGeneric, TargetName: target, CredentialBlobSize: uint32(len(blob)), CredentialBlob: &blob[0], Persist: 3, UserName: user}
	ok, _, _ := procCredWrite.Call(uintptr(unsafe.Pointer(&item)), 0)
	return ok != 0
}
func platformCredentialDelete(context.Context) bool {
	target, _ := windows.UTF16PtrFromString("gemini:antigravity")
	ok, _, err := procCredDelete.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0)
	return ok != 0 || err == syscall.ERROR_NOT_FOUND
}
