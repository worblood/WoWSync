//go:build windows

package wow

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

func listProcesses() ([]string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return nil, err
	}
	var names []string
	for {
		names = append(names, windows.UTF16ToString(e.ExeFile[:]))
		if err := windows.Process32Next(snap, &e); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return names, nil
			}
			return names, err
		}
	}
}
