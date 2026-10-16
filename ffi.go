package main

import "unsafe"

// ffi.go is where every raw address this program turns back into a Go value
// lives, in one place, so the reason is written down once.
//
// The syscall layer returns uintptr, and Win32 hands out addresses in return
// values: a locked HGLOBAL, a COM interface pointer, a vtable slot. Such an
// address has to become a Go pointer in order to be read. `go vet`'s unsafeptr
// check flags that conversion, and rightly so in general — it is exactly the
// pattern that breaks if a GC moves something in between.
//
// It is safe here for a reason specific to each call, and each helper below names
// it: the memory is owned by the operating system, is not Go-managed, and stays
// valid until the matching unlock or release. Nothing in these helpers holds the
// result beyond that window.
//
// The consequence is that the vet invocation for this module disables that one
// check, and only that one:
//
//	go vet -unsafeptr=false ./...
//
// build.ps1 runs exactly that, so the check is not quietly skipped: it is
// excluded with its reason recorded here and in the README.

// lockedBytes views n bytes at an address returned by GlobalLock. The caller must
// hold the lock (that is, must not have called GlobalUnlock yet) for as long as
// the slice is used. Windows will not move the block while it is locked.
func lockedBytes(addr uintptr, n int) []byte {
	if addr == 0 || n <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(addr)), n)
}

// comVTableSlot reads a method pointer out of a COM vtable. index 0 is
// QueryInterface, 1 AddRef, 2 Release.
func comVTableSlot(obj uintptr, index int) uintptr {
	if obj == 0 {
		return 0
	}
	vtable := *(*uintptr)(unsafe.Pointer(obj))
	return *(*uintptr)(unsafe.Pointer(vtable + uintptr(index)*unsafe.Sizeof(uintptr(0))))
}

// u16At views n UTF-16 units at an address, used by the registry and the shell's
// item lists.
func u16At(addr uintptr, n int) []uint16 {
	if addr == 0 || n <= 0 {
		return nil
	}
	return unsafe.Slice((*uint16)(unsafe.Pointer(addr)), n)
}

// comReleaseNow releases a COM object through its vtable, which is what
// drag.go needs for the data object the shell hands back from OleGetClipboard.
func comReleaseNow(obj uintptr) {
	if obj == 0 {
		return
	}
	release := comVTableSlot(obj, 2)
	callProcAddr(release, obj)
}

// releaseStreamNow releases an IStream the same way.
func releaseStreamNow(stream uintptr) {
	if stream == 0 {
		return
	}
	release := comVTableSlot(stream, 2)
	callProcAddr(release, stream)
}
