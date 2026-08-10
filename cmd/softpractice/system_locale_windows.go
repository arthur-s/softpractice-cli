//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var getUserDefaultLocaleName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

func systemLocaleName() string {
	var name [85]uint16 // LOCALE_NAME_MAX_LENGTH from the Windows API.
	result, _, _ := getUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&name[0])),
		uintptr(len(name)),
	)
	if result == 0 {
		return ""
	}
	return syscall.UTF16ToString(name[:])
}
