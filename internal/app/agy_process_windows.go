//go:build windows

package app

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// runningAgyProcesses counts agy.exe processes via a Toolhelp snapshot.
func runningAgyProcesses() int {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	if windows.Process32First(snapshot, &entry) != nil {
		return 0
	}
	count := 0
	for {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "agy.exe") {
			count++
		}
		if windows.Process32Next(snapshot, &entry) != nil {
			break
		}
	}
	return count
}
